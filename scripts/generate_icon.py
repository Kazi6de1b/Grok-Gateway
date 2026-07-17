from pathlib import Path

from PIL import Image, ImageDraw, ImageFont


SIZE = 256
root = Path(__file__).resolve().parents[1]
output = root / "build" / "windows" / "icon.ico"
output.parent.mkdir(parents=True, exist_ok=True)

canvas = Image.new("RGBA", (SIZE, SIZE), (0, 0, 0, 0))
gradient = Image.new("RGBA", (SIZE, SIZE))
pixels = gradient.load()
top = (198, 255, 111)
bottom = (91, 170, 35)
for y in range(SIZE):
    ratio = y / (SIZE - 1)
    colour = tuple(round(top[i] * (1 - ratio) + bottom[i] * ratio) for i in range(3))
    for x in range(SIZE):
        glow = max(0, 1 - (((x - 78) ** 2 + (y - 54) ** 2) ** 0.5) / 230)
        pixels[x, y] = tuple(min(255, round(c + 20 * glow)) for c in colour) + (255,)

mask = Image.new("L", (SIZE, SIZE), 0)
ImageDraw.Draw(mask).rounded_rectangle((18, 18, 238, 238), radius=58, fill=255)
canvas.paste(gradient, (0, 0), mask)

draw = ImageDraw.Draw(canvas)
font_path = Path("C:/Windows/Fonts/seguisb.ttf")
font = ImageFont.truetype(str(font_path), 126)
box = draw.textbbox((0, 0), "G", font=font)
text_width = box[2] - box[0]
text_height = box[3] - box[1]
draw.text(
    ((SIZE - text_width) / 2, (SIZE - text_height) / 2 - box[1] - 3),
    "G",
    font=font,
    fill=(13, 23, 8, 255),
)

canvas.save(output, format="ICO", sizes=[(16, 16), (24, 24), (32, 32), (48, 48), (64, 64), (128, 128), (256, 256)])
print(output)
