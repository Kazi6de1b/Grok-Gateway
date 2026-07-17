package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"grok-gateway/internal/account"
	"grok-gateway/internal/admin"
	"grok-gateway/internal/config"
	"grok-gateway/internal/observe"
	gatewayproxy "grok-gateway/internal/proxy"
)

var version = "dev"

func main() {
	defaultRun := len(os.Args) == 1
	if err := runMain(); err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		if defaultRun {
			showFatalError("Grok Gateway", err.Error())
		}
		os.Exit(1)
	}
}

func runMain() error {
	command, commandArgs, configPath, err := parseArguments(os.Args[1:])
	if err != nil {
		return err
	}
	if command == "help" {
		printHelp()
		return nil
	}
	if command == "version" {
		fmt.Println("grok-gateway", version)
		return nil
	}
	store, created, err := config.LoadOrCreate(configPath)
	if err != nil {
		return err
	}
	if created {
		fmt.Println("已生成默认配置:", store.Path())
	}
	cfg := store.Snapshot()
	client, err := newHTTPClient(cfg.OutboundProxy)
	if err != nil {
		return err
	}
	oauth := account.OAuthClient{HTTP: client}

	switch command {
	case "login":
		return loginAccount(context.Background(), store, oauth)
	case "accounts":
		printAccounts(store.Snapshot())
		return nil
	case "serve":
		return serve(context.Background(), store, client, oauth)
	case "run":
		_ = commandArgs
		return runDesktop(store, client, oauth)
	default:
		return fmt.Errorf("未知命令 %q", command)
	}
}

func parseArguments(args []string) (command string, commandArgs []string, configPath string, err error) {
	configPath, err = defaultConfigPath()
	if err != nil {
		return "", nil, "", err
	}
	filtered := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		if args[index] == "--config" || args[index] == "-config" {
			if index+1 >= len(args) {
				return "", nil, "", errors.New("--config 缺少路径")
			}
			configPath = args[index+1]
			index++
			continue
		}
		filtered = append(filtered, args[index])
	}
	if len(filtered) == 0 {
		return "run", nil, configPath, nil
	}
	command = strings.ToLower(filtered[0])
	switch command {
	case "run", "serve", "login", "accounts", "help", "version":
		commandArgs = filtered[1:]
	default:
		// 未写子命令时，把参数直接交给 Grok Build。
		command, commandArgs = "run", filtered
	}
	if len(commandArgs) > 0 && commandArgs[0] == "--" {
		commandArgs = commandArgs[1:]
	}
	return command, commandArgs, configPath, nil
}

func defaultConfigPath() (string, error) {
	if value := strings.TrimSpace(os.Getenv("GROK_GATEWAY_CONFIG")); value != "" {
		return filepath.Abs(value)
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(executable), "config.json"), nil
}

func newHTTPClient(proxyAddress string) (*http.Client, error) {
	proxyURL, err := url.Parse(proxyAddress)
	if err != nil || proxyURL.Scheme == "" || proxyURL.Host == "" {
		return nil, fmt.Errorf("出站代理地址无效: %s", proxyAddress)
	}
	transport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL), ForceAttemptHTTP2: true,
		MaxIdleConns: 64, MaxIdleConnsPerHost: 32, IdleConnTimeout: 90 * time.Second,
		TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 90 * time.Second,
	}
	return &http.Client{Transport: transport}, nil
}

func loginAccount(ctx context.Context, store *config.Store, oauth account.OAuthClient) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()
	authorization, err := oauth.StartDevice(ctx)
	if err != nil {
		return fmt.Errorf("启动 Device OAuth 失败（请确认 127.0.0.1:7890 代理已启动）: %w", err)
	}
	verificationURL := authorization.VerificationURIComplete
	if verificationURL == "" {
		verificationURL = authorization.VerificationURI
	}
	fmt.Println("请在浏览器完成 Grok Build 授权：")
	fmt.Println("  地址:", verificationURL)
	fmt.Println("  验证码:", authorization.UserCode)
	_ = openBrowser(verificationURL)

	deadline := time.NewTimer(authorization.ExpiresIn)
	defer deadline.Stop()
	interval := authorization.Interval
	for {
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-deadline.C:
			timer.Stop()
			return errors.New("Device OAuth 已超时")
		case <-timer.C:
		}
		tokens, pollErr := oauth.PollDevice(ctx, authorization.DeviceCode)
		if errors.Is(pollErr, account.ErrAuthorizationPending) {
			continue
		}
		if errors.Is(pollErr, account.ErrSlowDown) {
			interval += 5 * time.Second
			continue
		}
		if pollErr != nil {
			return pollErr
		}
		value := account.AccountFromTokens("", tokens)
		if err := store.UpsertAccount(value); err != nil {
			return err
		}
		fmt.Printf("账号已保存: %s\n", value.Name)
		fmt.Println("提醒：config.json 含 OAuth 凭据，请勿上传或分享。")
		return nil
	}
}

func openBrowser(address string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", address).Start()
	case "darwin":
		return exec.Command("open", address).Start()
	default:
		return exec.Command("xdg-open", address).Start()
	}
}

func serve(ctx context.Context, store *config.Store, client *http.Client, oauth account.OAuthClient) error {
	server, listener, _, err := newServer(store, client, oauth)
	if err != nil {
		return err
	}
	fmt.Printf("Grok Gateway %s 正在监听 http://%s\n", version, store.Snapshot().Listen)
	fmt.Printf("Grok Build Base URL: http://%s/v1\n", store.Snapshot().Listen)
	fmt.Printf("上游流量通过: %s\n", store.Snapshot().OutboundProxy)
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		return server.Shutdown(shutdownCtx)
	}
}

func newServer(store *config.Store, client *http.Client, oauth account.OAuthClient) (*http.Server, net.Listener, http.Handler, error) {
	cfg := store.Snapshot()
	pool := account.NewPool(store, oauth)
	obs, err := observe.New(filepath.Join(filepath.Dir(store.Path()), "data"))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("初始化观测存储: %w", err)
	}
	proxyHandler, err := gatewayproxy.NewHandler(cfg, pool, client, slog.Default(), store, obs)
	if err != nil {
		return nil, nil, nil, err
	}
	handler, err := admin.NewHandler(proxyHandler, store, pool, client, oauth, version, slog.Default(), obs)
	if err != nil {
		return nil, nil, nil, err
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("监听 %s: %w", cfg.Listen, err)
	}
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 2 * time.Minute,
	}
	return server, listener, handler, nil
}

func printAccounts(cfg config.Config) {
	if len(cfg.Accounts) == 0 {
		fmt.Println("没有账号。运行 grok-gateway.exe login 添加账号。")
		return
	}
	for index, value := range cfg.Accounts {
		status := "启用"
		if !value.Enabled {
			status = "停用"
		}
		fmt.Printf("%d. %s  [%s]  到期: %s\n", index+1, value.Name, status, value.ExpiresAt.Local().Format(time.RFC3339))
	}
}

func printHelp() {
	fmt.Println(`Grok Gateway - Grok Build 本地账号池网关

用法:
	  grok-gateway.exe                 启动桌面 GUI 与本地网关
  grok-gateway.exe serve           启动网关但不自动打开界面
  grok-gateway.exe login           添加或更新一个 Grok Build 账号
  grok-gateway.exe accounts        查看账号列表
  grok-gateway.exe version         查看版本

全局选项:
  --config <路径>                  指定 config.json`)
}
