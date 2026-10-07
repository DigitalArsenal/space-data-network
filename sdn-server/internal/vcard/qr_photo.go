package vcard

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg" // photos arrive as JPEG or PNG data URLs
	"image/png"
	"sort"
	"strings"
	"sync"
)

// QRPhotoBudgetBytes is the size of a scannable card that carries a
// thumbnail. Measured against a real 560-byte card, 1,150 bytes renders at a
// density phones resolve in the hand at the 320–480 px the dashboard draws
// codes at (SpaceAware-UI data/photo.js QR_WITH_PHOTO_BUDGET_BYTES).
const QRPhotoBudgetBytes = 1150

// A photo is read only within these bounds; a larger one gets no thumbnail
// rather than an expensive decode on a public surface.
const (
	maxQRPhotoSourceBytes = 512 << 10
	maxQRPhotoSourceEdge  = 4096
)

// qrThumbnailSteps is the search order, best-looking first: the thumbnail's
// edge in pixels and its palette size. The first whose card fits wins.
var qrThumbnailSteps = []struct{ edge, colors int }{
	{48, 16}, {40, 16}, {36, 16}, {32, 16}, {40, 8}, {32, 8}, {28, 16}, {28, 8},
	{24, 16}, {24, 8}, {32, 4}, {24, 4}, {20, 16}, {20, 8}, {16, 16}, {16, 8}, {16, 4},
}

// WithQRPhoto puts a thumbnail of photoDataURL (a data:image/jpeg or
// data:image/png URL) on a compact card: the best-looking palette PNG that
// keeps the whole card within QRPhotoBudgetBytes. A full photo cannot share
// a scannable code, so the card is returned as it was when no thumbnail fits
// or the photo cannot be read — a code that does not scan is worse than one
// without a picture.
func WithQRPhoto(card, photoDataURL string) string {
	photoDataURL = strings.TrimSpace(photoDataURL)
	if card == "" || photoDataURL == "" || strings.Contains(card, "\nPHOTO") {
		return card
	}
	room := QRPhotoBudgetBytes - len(card)
	if room <= 0 {
		return card
	}
	key := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s", room, photoDataURL)))
	line, ok := qrPhotoLines.get(key)
	if !ok {
		line = qrPhotoLine(card, photoDataURL)
		qrPhotoLines.put(key, line)
	}
	if line == "" {
		return card
	}
	return insertRawVCardLinesCRLF(card, []string{line})
}

// qrPhotoLine returns the folded PHOTO line of the first thumbnail step whose
// card fits, or "" when none does.
func qrPhotoLine(card, photoDataURL string) string {
	src, ok := decodeQRPhoto(photoDataURL)
	if !ok {
		return ""
	}
	for _, step := range qrThumbnailSteps {
		var buf bytes.Buffer
		encoder := png.Encoder{CompressionLevel: png.BestCompression}
		if err := encoder.Encode(&buf, quantizeQRThumbnail(downsampleQRThumbnail(src, step.edge), step.colors)); err != nil {
			continue
		}
		line := foldVCardLine("PHOTO;ENCODING=b;TYPE=PNG:" + base64.StdEncoding.EncodeToString(buf.Bytes()))
		if len(insertRawVCardLinesCRLF(card, []string{line})) <= QRPhotoBudgetBytes {
			return line
		}
	}
	return ""
}

func decodeQRPhoto(dataURL string) (image.Image, bool) {
	comma := strings.IndexByte(dataURL, ',')
	if comma < 0 || !strings.HasPrefix(dataURL, "data:image/") || !strings.HasSuffix(dataURL[:comma], ";base64") {
		return nil, false
	}
	if (len(dataURL)-comma-1)/4*3 > maxQRPhotoSourceBytes {
		return nil, false
	}
	raw, err := base64.StdEncoding.DecodeString(dataURL[comma+1:])
	if err != nil {
		return nil, false
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || config.Width <= 0 || config.Height <= 0 ||
		config.Width > maxQRPhotoSourceEdge || config.Height > maxQRPhotoSourceEdge {
		return nil, false
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	return img, err == nil
}

// downsampleQRThumbnail crops the photo's centre square and averages it down
// to edge×edge, flattened onto white (a contact photo has no transparency).
// Each cell averages at most 8×8 samples, which is plenty for a thumbnail and
// keeps a large photo cheap.
func downsampleQRThumbnail(src image.Image, edge int) *image.NRGBA {
	bounds := src.Bounds()
	side := min(bounds.Dx(), bounds.Dy())
	x0 := bounds.Min.X + (bounds.Dx()-side)/2
	y0 := bounds.Min.Y + (bounds.Dy()-side)/2
	out := image.NewNRGBA(image.Rect(0, 0, edge, edge))
	for y := 0; y < edge; y++ {
		top, bottom := y0+y*side/edge, max(y0+(y+1)*side/edge, y0+y*side/edge+1)
		for x := 0; x < edge; x++ {
			left, right := x0+x*side/edge, max(x0+(x+1)*side/edge, x0+x*side/edge+1)
			var r, g, b, n uint64
			for sy := top; sy < bottom; sy += max(1, (bottom-top)/8) {
				for sx := left; sx < right; sx += max(1, (right-left)/8) {
					cr, cg, cb, ca := src.At(sx, sy).RGBA()
					r += uint64(cr + 0xffff - ca)
					g += uint64(cg + 0xffff - ca)
					b += uint64(cb + 0xffff - ca)
					n++
				}
			}
			out.SetNRGBA(x, y, color.NRGBA{uint8((r / n) >> 8), uint8((g / n) >> 8), uint8((b / n) >> 8), 0xff})
		}
	}
	return out
}

// quantizeQRThumbnail reduces the thumbnail to at most `colors` colors by
// median cut, so it encodes as a 1-, 2- or 4-bit palette PNG.
func quantizeQRThumbnail(img *image.NRGBA, colors int) *image.Paletted {
	bounds := img.Bounds()
	pixels := make([][3]uint8, 0, bounds.Dx()*bounds.Dy())
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			c := img.NRGBAAt(x, y)
			pixels = append(pixels, [3]uint8{c.R, c.G, c.B})
		}
	}
	boxes := [][][3]uint8{pixels}
	for len(boxes) < colors {
		split, channel, widest := -1, 0, 0
		for i, box := range boxes {
			for ch := 0; ch < 3; ch++ {
				lo, hi := uint8(255), uint8(0)
				for _, p := range box {
					lo, hi = min(lo, p[ch]), max(hi, p[ch])
				}
				if int(hi)-int(lo) > widest {
					split, channel, widest = i, ch, int(hi)-int(lo)
				}
			}
		}
		if split < 0 {
			break // every box is a single color
		}
		box := boxes[split]
		sort.Slice(box, func(a, b int) bool { return box[a][channel] < box[b][channel] })
		boxes[split] = box[:len(box)/2]
		boxes = append(boxes, box[len(box)/2:])
	}
	palette := make(color.Palette, len(boxes))
	for i, box := range boxes {
		var sum [3]int
		for _, p := range box {
			sum[0], sum[1], sum[2] = sum[0]+int(p[0]), sum[1]+int(p[1]), sum[2]+int(p[2])
		}
		palette[i] = color.RGBA{uint8(sum[0] / len(box)), uint8(sum[1] / len(box)), uint8(sum[2] / len(box)), 0xff}
	}
	out := image.NewPaletted(bounds, palette)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			out.SetColorIndex(x, y, uint8(palette.Index(img.NRGBAAt(x, y))))
		}
	}
	return out
}

// qrPhotoLines remembers each photo's thumbnail line per card size, so a
// public card is not re-encoded on every request.
var qrPhotoLines = &qrPhotoLineCache{lines: map[[32]byte]string{}}

type qrPhotoLineCache struct {
	mu    sync.Mutex
	lines map[[32]byte]string
}

func (c *qrPhotoLineCache) get(key [32]byte) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	line, ok := c.lines[key]
	return line, ok
}

func (c *qrPhotoLineCache) put(key [32]byte, line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.lines) >= 256 {
		c.lines = map[[32]byte]string{}
	}
	c.lines[key] = line
}
