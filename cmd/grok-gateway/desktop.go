package main

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"grok-gateway/internal/account"
	"grok-gateway/internal/config"
)

func runDesktop(store *config.Store, client *http.Client, oauth account.OAuthClient) error {
	server, listener, handler, err := newServer(store, client, oauth)
	if err != nil {
		return err
	}
	var shutdownOnce sync.Once
	shutdown := func() {
		shutdownOnce.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(ctx)
		})
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()
	var desktopContext context.Context
	var desktopContextMu sync.RWMutex

	// Compact default: fits a full overview without forcing users to widen
	// the window. ZoomFactor < 1 densifies the layout so more content fits
	// on typical 1080p / 125% DPI screens without occupying half the desktop.
	err = wails.Run(&options.App{
		Title:                    "Grok Gateway",
		Width:                    1020,
		Height:                   700,
		MinWidth:                 860,
		MinHeight:                580,
		BackgroundColour:         options.NewRGB(8, 10, 15),
		AssetServer:              &assetserver.Options{Handler: handler},
		EnableDefaultContextMenu: false,
		WindowStartState:         options.Normal,
		OnStartup: func(ctx context.Context) {
			desktopContextMu.Lock()
			desktopContext = ctx
			desktopContextMu.Unlock()
			// Center after creation so multi-monitor DPI setups land correctly.
			wailsruntime.WindowCenter(ctx)
			go func() {
				serveErr := <-serveResult
				if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
					_, _ = wailsruntime.MessageDialog(ctx, wailsruntime.MessageDialogOptions{
						Type: wailsruntime.ErrorDialog, Title: "本地网关异常", Message: serveErr.Error(),
					})
					wailsruntime.Quit(ctx)
				}
			}()
		},
		OnShutdown: func(context.Context) { shutdown() },
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "com.local.grok-gateway",
			OnSecondInstanceLaunch: func(_ options.SecondInstanceData) {
				desktopContextMu.RLock()
				ctx := desktopContext
				desktopContextMu.RUnlock()
				if ctx != nil {
					wailsruntime.WindowShow(ctx)
					wailsruntime.WindowUnminimise(ctx)
					wailsruntime.WindowCenter(ctx)
				}
			},
		},
		Windows: &windows.Options{
			Theme: windows.Dark, BackdropType: windows.Mica,
			WebviewIsTransparent: false, WindowIsTranslucent: false,
			// Slight zoom-out so the dashboard fits the compact default window.
			// Ctrl+wheel / pinch remain disabled so users cannot accidentally re-zoom.
			IsZoomControlEnabled: false,
			DisablePinchZoom:     true,
			ZoomFactor:           0.92,
			ResizeDebounceMS:     16,
			Messages: &windows.Messages{
				InstallationRequired: "Grok Gateway 需要 Microsoft WebView2 Runtime。按确定后安装。",
				UpdateRequired:       "Microsoft WebView2 Runtime 版本过旧，需要更新。",
				MissingRequirements:  "缺少运行组件", Webview2NotInstalled: "未安装 WebView2 Runtime",
				Error: "错误", FailedToInstall: "WebView2 Runtime 安装失败。",
				DownloadPage: "请安装 Microsoft WebView2 Runtime。", PressOKToInstall: "按确定开始安装。",
				ContactAdmin:         "请安装 Microsoft WebView2 Runtime 后重试。",
				InvalidFixedWebview2: "WebView2 Runtime 路径无效。",
				WebView2ProcessCrash: "WebView2 进程异常退出，请重启 Grok Gateway。",
			},
		},
	})
	shutdown()
	return err
}
