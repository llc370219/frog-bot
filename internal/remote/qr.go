package remote

import (
	"bytes"
	"errors"
	"fmt"
)

// EncodeQRSVG 使用标准 ISO/IEC 18004 Byte 模式（纠错等级 L，Version 1..12）生成无第三方依赖的纯矢量二维码 SVG。
func EncodeQRSVG(text string) (string, error) {
	grid, err := EncodeQRMatrix([]byte(text))
	if err != nil {
		return "", err
	}
	n := len(grid)
	const quiet = 4
	total := n + quiet*2
	var b bytes.Buffer
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges">`, total, total)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#ffffff"/>`, total, total)
	b.WriteString(`<path fill="#111111" d="`)
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if grid[y][x] {
				fmt.Fprintf(&b, "M%d,%dh1v1h-1z", x+quiet, y+quiet)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return b.String(), nil
}

type qrVersion struct {
	ver        int
	totalData  int
	ecPerBlock int
	blocksG1   int
	dataG1     int
	blocksG2   int
	dataG2     int
	align      []int
}

// ISO/IEC 18004 Level L 参数表（Version 1 ~ 12，最大支持 366 字节）
var qrVersionsL = []qrVersion{
	{1, 19, 7, 1, 19, 0, 0, nil},
	{2, 34, 10, 1, 34, 0, 0, []int{6, 18}},
	{3, 55, 15, 1, 55, 0, 0, []int{6, 22}},
	{4, 80, 20, 1, 80, 0, 0, []int{6, 26}},
	{5, 108, 26, 1, 108, 0, 0, []int{6, 30}},
	{6, 136, 18, 2, 68, 0, 0, []int{6, 34}},
	{7, 156, 20, 2, 78, 0, 0, []int{6, 22, 38}},
	{8, 194, 24, 2, 97, 0, 0, []int{6, 24, 42}},
	{9, 232, 30, 2, 116, 0, 0, []int{6, 26, 46}},
	{10, 274, 18, 2, 68, 2, 69, []int{6, 28, 50}},
	{11, 324, 20, 4, 81, 0, 0, []int{6, 30, 54}},
	{12, 370, 24, 2, 92, 2, 93, []int{6, 32, 58}},
}

var versionInfoBits = map[int]uint32{
	7:  0x07C94,
	8:  0x085BC,
	9:  0x09A99,
	10: 0x0A4D3,
	11: 0x0BBF6,
	12: 0x0C762,
}

func EncodeQRMatrix(payload []byte) ([][]bool, error) {
	var spec qrVersion
	found := false
	for _, v := range qrVersionsL {
		lenBits := 8
		if v.ver >= 10 {
			lenBits = 16
		}
		capBytes := (v.totalData*8 - 4 - lenBits) / 8
		if len(payload) <= capBytes {
			spec = v
			found = true
			break
		}
	}
	if !found {
		return nil, errors.New("配对码长度超出二维码上限")
	}

	dataCW := encodeDataCodewords(payload, spec)
	finalCW := interleaveAndEC(dataCW, spec)

	size := 17 + 4*spec.ver
	modules := make([][]bool, size)
	isFunc := make([][]bool, size)
	for i := range size {
		modules[i] = make([]bool, size)
		isFunc[i] = make([]bool, size)
	}

	setFunc := func(x, y int, dark bool) {
		if x >= 0 && x < size && y >= 0 && y < size {
			modules[y][x] = dark
			isFunc[y][x] = true
		}
	}

	drawFinder := func(cx, cy int) {
		for dy := -4; dy <= 4; dy++ {
			for dx := -4; dx <= 4; dx++ {
				xx, yy := cx+dx, cy+dy
				if xx < 0 || xx >= size || yy < 0 || yy >= size {
					continue
				}
				dist := max(abs(dx), abs(dy))
				setFunc(xx, yy, dist != 2 && dist != 4)
			}
		}
	}
	drawFinder(3, 3)
	drawFinder(size-4, 3)
	drawFinder(3, size-4)

	for i := 0; i < size; i++ {
		if !isFunc[6][i] {
			setFunc(i, 6, i%2 == 0)
		}
		if !isFunc[i][6] {
			setFunc(6, i, i%2 == 0)
		}
	}

	for _, ay := range spec.align {
		for _, ax := range spec.align {
			if (ax == 6 && ay == 6) || (ax == 6 && ay == size-7) || (ax == size-7 && ay == 6) {
				continue
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					setFunc(ax+dx, ay+dy, max(abs(dx), abs(dy)) != 1)
				}
			}
		}
	}

	// 预留格式信息区与暗模块
	for i := 0; i <= 8; i++ {
		if i != 6 {
			if !isFunc[8][i] {
				setFunc(i, 8, false)
			}
			if !isFunc[i][8] {
				setFunc(8, i, false)
			}
		}
	}
	for i := 0; i < 8; i++ {
		setFunc(size-1-i, 8, false)
	}
	for i := 0; i < 7; i++ {
		setFunc(8, size-1-i, false)
	}
	setFunc(8, size-8, true)

	if vbits, ok := versionInfoBits[spec.ver]; ok {
		for i := 0; i < 18; i++ {
			bit := ((vbits >> i) & 1) == 1
			a := size - 11 + i%3
			b := i / 3
			setFunc(a, b, bit)
			setFunc(b, a, bit)
		}
	}

	// 写入数据与纠错位（蛇形双列）
	bitIdx := 0
	totalBits := len(finalCW) * 8
	for right := size - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for vert := 0; vert < size; vert++ {
			for j := 0; j < 2; j++ {
				x := right - j
				upward := ((right + 1) & 2) == 0
				y := vert
				if upward {
					y = size - 1 - vert
				}
				if !isFunc[y][x] && bitIdx < totalBits {
					dark := ((finalCW[bitIdx>>3] >> (7 - (bitIdx & 7))) & 1) == 1
					modules[y][x] = dark
					bitIdx++
				}
			}
		}
	}

	// 选择惩罚最低的掩码 (0..7)
	bestMask := 0
	bestPenalty := int(^uint(0) >> 1)
	for mask := 0; mask < 8; mask++ {
		cand := cloneGrid(modules)
		applyMask(cand, isFunc, mask)
		drawFormatBits(cand, mask)
		if p := scorePenalty(cand); p < bestPenalty {
			bestPenalty = p
			bestMask = mask
		}
	}
	applyMask(modules, isFunc, bestMask)
	drawFormatBits(modules, bestMask)
	return modules, nil
}

func encodeDataCodewords(payload []byte, spec qrVersion) []byte {
	var bits []bool
	appendBits := func(val, n int) {
		for i := n - 1; i >= 0; i-- {
			bits = append(bits, ((val>>i)&1) == 1)
		}
	}
	appendBits(0b0100, 4) // Byte mode
	if spec.ver >= 10 {
		appendBits(len(payload), 16)
	} else {
		appendBits(len(payload), 8)
	}
	for _, b := range payload {
		appendBits(int(b), 8)
	}
	capBits := spec.totalData * 8
	for i := 0; i < 4 && len(bits) < capBits; i++ {
		bits = append(bits, false)
	}
	for len(bits)%8 != 0 {
		bits = append(bits, false)
	}
	out := make([]byte, 0, spec.totalData)
	for i := 0; i < len(bits); i += 8 {
		var v byte
		for j := 0; j < 8; j++ {
			v <<= 1
			if bits[i+j] {
				v |= 1
			}
		}
		out = append(out, v)
	}
	pads := [2]byte{0xEC, 0x11}
	for i := 0; len(out) < spec.totalData; i++ {
		out = append(out, pads[i%2])
	}
	return out
}

func interleaveAndEC(data []byte, spec qrVersion) []byte {
	var dataBlocks [][]byte
	var ecBlocks [][]byte
	gen := rsGeneratorPoly(spec.ecPerBlock)
	offset := 0
	for i := 0; i < spec.blocksG1; i++ {
		blk := data[offset : offset+spec.dataG1]
		offset += spec.dataG1
		dataBlocks = append(dataBlocks, blk)
		ecBlocks = append(ecBlocks, rsComputeRemainder(blk, gen))
	}
	for i := 0; i < spec.blocksG2; i++ {
		blk := data[offset : offset+spec.dataG2]
		offset += spec.dataG2
		dataBlocks = append(dataBlocks, blk)
		ecBlocks = append(ecBlocks, rsComputeRemainder(blk, gen))
	}
	maxData := max(spec.dataG1, spec.dataG2)
	out := make([]byte, 0, spec.totalData+len(dataBlocks)*spec.ecPerBlock)
	for i := 0; i < maxData; i++ {
		for _, blk := range dataBlocks {
			if i < len(blk) {
				out = append(out, blk[i])
			}
		}
	}
	for i := 0; i < spec.ecPerBlock; i++ {
		for _, blk := range ecBlocks {
			out = append(out, blk[i])
		}
	}
	return out
}

func gfMul(x, y byte) byte {
	var z byte
	for i := 7; i >= 0; i-- {
		z = (z << 1) ^ ((z >> 7) * 0x1D)
		if ((y >> i) & 1) != 0 {
			z ^= x
		}
	}
	return z
}

func rsGeneratorPoly(degree int) []byte {
	res := make([]byte, degree)
	res[degree-1] = 1
	var root byte = 1
	for i := 0; i < degree; i++ {
		for j := 0; j < degree; j++ {
			res[j] = gfMul(res[j], root)
			if j+1 < degree {
				res[j] ^= res[j+1]
			}
		}
		root = gfMul(root, 0x02)
	}
	return res
}

func rsComputeRemainder(data, gen []byte) []byte {
	res := make([]byte, len(gen))
	for _, b := range data {
		factor := b ^ res[0]
		copy(res, res[1:])
		res[len(res)-1] = 0
		for j, g := range gen {
			res[j] ^= gfMul(g, factor)
		}
	}
	return res
}

func applyMask(modules, isFunc [][]bool, mask int) {
	size := len(modules)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if isFunc[y][x] {
				continue
			}
			var inv bool
			switch mask {
			case 0:
				inv = (x+y)%2 == 0
			case 1:
				inv = y%2 == 0
			case 2:
				inv = x%3 == 0
			case 3:
				inv = (x+y)%3 == 0
			case 4:
				inv = (y/2+x/3)%2 == 0
			case 5:
				inv = (x*y)%2+(x*y)%3 == 0
			case 6:
				inv = ((x*y)%2+(x*y)%3)%2 == 0
			case 7:
				inv = ((x+y)%2+(x*y)%3)%2 == 0
			}
			if inv {
				modules[y][x] = !modules[y][x]
			}
		}
	}
}

// Level L indicator = 01
func drawFormatBits(modules [][]bool, mask int) {
	size := len(modules)
	data := (0b01 << 3) | mask
	rem := data
	for i := 0; i < 10; i++ {
		rem = (rem << 1) ^ ((rem >> 9) * 0x537)
	}
	bits := ((data << 10) | rem) ^ 0x5412
	bit := func(i int) bool { return ((bits >> i) & 1) == 1 }

	for i := 0; i <= 5; i++ {
		modules[8][i] = bit(14 - i)
	}
	modules[8][7] = bit(8)
	modules[8][8] = bit(7)
	modules[7][8] = bit(6)
	for i := 9; i < 15; i++ {
		modules[14-i][8] = bit(14 - i)
	}

	for i := 0; i < 8; i++ {
		modules[8][size-1-i] = bit(i)
	}
	for i := 8; i < 15; i++ {
		modules[size-15+i][8] = bit(i)
	}
	modules[size-8][8] = true
}

func scorePenalty(m [][]bool) int {
	n := len(m)
	p := 0
	for y := 0; y < n; y++ {
		runColor := m[y][0]
		runLen := 1
		for x := 1; x < n; x++ {
			if m[y][x] == runColor {
				runLen++
				if runLen == 5 {
					p += 3
				} else if runLen > 5 {
					p++
				}
			} else {
				runColor = m[y][x]
				runLen = 1
			}
		}
	}
	for x := 0; x < n; x++ {
		runColor := m[0][x]
		runLen := 1
		for y := 1; y < n; y++ {
			if m[y][x] == runColor {
				runLen++
				if runLen == 5 {
					p += 3
				} else if runLen > 5 {
					p++
				}
			} else {
				runColor = m[y][x]
				runLen = 1
			}
		}
	}
	for y := 0; y < n-1; y++ {
		for x := 0; x < n-1; x++ {
			c := m[y][x]
			if c == m[y][x+1] && c == m[y+1][x] && c == m[y+1][x+1] {
				p += 3
			}
		}
	}
	return p
}

func cloneGrid(src [][]bool) [][]bool {
	out := make([][]bool, len(src))
	for i := range src {
		out[i] = make([]bool, len(src[i]))
		copy(out[i], src[i])
	}
	return out
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
