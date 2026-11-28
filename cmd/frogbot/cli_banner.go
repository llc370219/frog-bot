package main

import (
	"fmt"
	"os"
	"strings"
)

// 16x10 像素青蛙（每两行合成一行 Unicode 半块字符，共 5 行，风格对齐 agy 终端方块徽标）
// 颜色代号：
//   . 透明
//   G 翠绿主体 (256-color 77)
//   D 深绿轮廓/微笑/脚掌 (256-color 28)
//   L 浅绿肚皮 (256-color 157)
//   W 眼白 (256-color 231)
//   K 瞳孔 (256-color 233)
//   P 腮红 (256-color 211)
var frogPixelArt = []string{
	"..GGGG....GGGG..",
	".GWWWWGGGGWWWWG.",
	".GWKKWGGGGWKKWG.",
	".GWWWWGGGGWWWWG.",
	"GGGGGGGGGGGGGGGG",
	"GGPGGGGGGGGGGPGG",
	"GGGDDDDDDDDDDGGG",
	".GGLLLLLLLLLLGG.",
	"..GGGGLLLLGGGG..",
	".DDD........DDD.",
}

var frogPalette256 = map[byte]int{
	'G': 77,
	'D': 28,
	'L': 157,
	'W': 231,
	'K': 233,
	'P': 211,
}

func useANSIColor() bool {
	if os.Getenv("NO_COLOR") != "" || strings.EqualFold(os.Getenv("TERM"), "dumb") {
		return false
	}
	return isTerminal(os.Stdout)
}

// renderFrogLogoLines 将像素青蛙渲染为 5 行终端半块方块画。
func renderFrogLogoLines(color bool) []string {
	rows := len(frogPixelArt)
	cols := len(frogPixelArt[0])
	out := make([]string, 0, (rows+1)/2)

	for y := 0; y < rows; y += 2 {
		var b strings.Builder
		for x := 0; x < cols; x++ {
			top := frogPixelArt[y][x]
			bot := byte('.')
			if y+1 < rows {
				bot = frogPixelArt[y+1][x]
			}

			if !color {
				// 无色终端保留瞳孔与微笑挖空轮廓，保证清晰辨识
				tOn := top != '.' && top != 'K' && top != 'D'
				bOn := bot != '.' && bot != 'K' && bot != 'D'
				if y == rows-2 {
					bOn = bot != '.'
				}
				switch {
				case tOn && bOn:
					b.WriteString("█")
				case tOn:
					b.WriteString("▀")
				case bOn:
					b.WriteString("▄")
				default:
					b.WriteByte(' ')
				}
				continue
			}

			tc, tHas := frogPalette256[top]
			bc, bHas := frogPalette256[bot]
			switch {
			case !tHas && !bHas:
				b.WriteString("\x1b[0m ")
			case tHas && !bHas:
				fmt.Fprintf(&b, "\x1b[0;38;5;%dm▀", tc)
			case !tHas && bHas:
				fmt.Fprintf(&b, "\x1b[0;38;5;%dm▄", bc)
			case tc == bc:
				fmt.Fprintf(&b, "\x1b[0;38;5;%dm█", tc)
			default:
				fmt.Fprintf(&b, "\x1b[38;5;%d;48;5;%dm▀", tc, bc)
			}
		}
		if color {
			b.WriteString("\x1b[0m")
		}
		out = append(out, b.String())
	}
	return out
}

// renderFrogBanner 把 5 行方块青蛙画在左侧，右侧并排展示状态或标题文本。
func renderFrogBanner(infoLines []string, color bool) string {
	logo := renderFrogLogoLines(color)
	n := len(logo)
	if len(infoLines) > n {
		n = len(infoLines)
	}
	const padEmpty = "                " // 16 列占位
	var b strings.Builder
	for i := 0; i < n; i++ {
		left := padEmpty
		if i < len(logo) {
			left = logo[i]
		}
		right := ""
		if i < len(infoLines) {
			right = infoLines[i]
		}
		if right != "" {
			fmt.Fprintf(&b, "  %s   %s\n", left, right)
		} else {
			fmt.Fprintf(&b, "  %s\n", left)
		}
	}
	return b.String()
}
