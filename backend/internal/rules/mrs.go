package rules

// MRS is mihomo's binary rule-set format (rules/provider/mrs_*.go upstream).
//
//	zstd( "MRS\x01" | behavior:u8 | count:i64 | extraLen:i64 | extra | body )
//
// The domain body is a succinct (LOUDS) trie of reversed keys, the ipcidr
// body a sorted list of [from, to] ranges as 16-byte addresses. Every
// integer is big-endian. Only domain and ipcidr behaviors exist.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"sort"
	"strings"

	"github.com/klauspost/compress/zstd"
	"go4.org/netipx"
)

var (
	mrsMagic  = [4]byte{'M', 'R', 'S', 1}
	zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}
)

const (
	mrsBehaviorDomain byte = 0
	mrsBehaviorIPCIDR byte = 1
	// MaxMRSDecoded caps the decompressed size of an MRS file.
	MaxMRSDecoded = 512 << 20
)

var (
	ErrMRSEmpty    = errors.New("набор пуст: в MRS нельзя записать ноль правил")
	ErrMRSBehavior = errors.New("MRS поддерживает только behavior domain и ipcidr")
)

type MRS struct {
	Behavior Behavior
	Count    int64 // rule count stored in the header
	Domains  []string
	Prefixes []netip.Prefix
}

func (m *MRS) Len() int { return len(m.Domains) + len(m.Prefixes) }

// IsMRS reports whether data looks like a zstd stream that starts with the MRS magic.
func IsMRS(data []byte) bool {
	if !bytes.HasPrefix(data, zstdMagic) {
		return false
	}
	dec, err := zstd.NewReader(bytes.NewReader(data), zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(64<<20))
	if err != nil {
		return false
	}
	defer dec.Close()
	var magic [4]byte
	if _, err := io.ReadFull(dec, magic[:]); err != nil {
		return false
	}
	return magic == mrsMagic
}

// EncodeDomainMRS writes domain rules (canonical form) as MRS.
func EncodeDomainMRS(domainRules []string) ([]byte, error) {
	if len(domainRules) == 0 {
		return nil, ErrMRSEmpty
	}
	ds := buildDomainSet(domainKeys(domainRules))
	var body bytes.Buffer
	body.WriteByte(1) // body version
	writeWords(&body, ds.leaves)
	writeWords(&body, ds.labelBitmap)
	writeI64(&body, int64(len(ds.labels)))
	body.Write(ds.labels)
	return writeMRS(mrsBehaviorDomain, int64(len(domainRules)), body.Bytes())
}

// EncodeIPMRS writes CIDRs as MRS. Overlaps are merged as mihomo does.
func EncodeIPMRS(prefixes []netip.Prefix) ([]byte, error) {
	if len(prefixes) == 0 {
		return nil, ErrMRSEmpty
	}
	var b netipx.IPSetBuilder
	for _, p := range prefixes {
		b.AddPrefix(p)
	}
	set, err := b.IPSet()
	if err != nil {
		return nil, err
	}
	ranges := set.Ranges()
	var body bytes.Buffer
	body.WriteByte(1)
	writeI64(&body, int64(len(ranges)))
	for _, r := range ranges {
		from, to := r.From().As16(), r.To().As16()
		body.Write(from[:])
		body.Write(to[:])
	}
	return writeMRS(mrsBehaviorIPCIDR, int64(len(prefixes)), body.Bytes())
}

func writeMRS(behavior byte, count int64, body []byte) ([]byte, error) {
	var out bytes.Buffer
	enc, err := zstd.NewWriter(&out, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		return nil, err
	}
	var hdr bytes.Buffer
	hdr.Write(mrsMagic[:])
	hdr.WriteByte(behavior)
	writeI64(&hdr, count)
	writeI64(&hdr, 0) // extra: reserved upstream, always empty
	if _, err := enc.Write(hdr.Bytes()); err != nil {
		return nil, err
	}
	if _, err := enc.Write(body); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// DecodeMRS parses an MRS file. Sizes in the stream are checked before any
// allocation, so a hostile file cannot make us allocate unbounded memory.
func DecodeMRS(data []byte) (*MRS, error) {
	dec, err := zstd.NewReader(bytes.NewReader(data), zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(MaxMRSDecoded))
	if err != nil {
		return nil, fmt.Errorf("MRS: %w", err)
	}
	defer dec.Close()
	r := &limitedReader{r: dec, left: MaxMRSDecoded}

	var magic [4]byte
	if _, err := io.ReadFull(r, magic[:]); err != nil {
		return nil, fmt.Errorf("MRS: не удалось прочитать заголовок: %w", err)
	}
	if magic != mrsMagic {
		return nil, errors.New("MRS: неверная сигнатура файла")
	}
	var hdr [1]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	m := &MRS{}
	if m.Count, err = readI64(r); err != nil {
		return nil, err
	}
	extra, err := readI64(r)
	if err != nil {
		return nil, err
	}
	if extra < 0 || extra > r.left {
		return nil, errors.New("MRS: повреждён заголовок")
	}
	if _, err := io.CopyN(io.Discard, r, extra); err != nil {
		return nil, err
	}
	switch hdr[0] {
	case mrsBehaviorDomain:
		m.Behavior = BehaviorDomain
		m.Domains, err = readDomainSet(r)
	case mrsBehaviorIPCIDR:
		m.Behavior = BehaviorIPCIDR
		m.Prefixes, err = readIPRanges(r)
	default:
		return nil, fmt.Errorf("MRS: неизвестный behavior %d", hdr[0])
	}
	if err != nil {
		return nil, fmt.Errorf("MRS: %w", err)
	}
	return m, nil
}

// ---- domain trie -----------------------------------------------------------

// domainKeys mirrors what mihomo's DomainTrie.Foreach yields before the set
// is built: "+.x" expands to "x" and "+.x", while ".x" is stored as "+.x".
func domainKeys(rules []string) []string {
	set := make(map[string]struct{}, len(rules)*2)
	for _, r := range rules {
		switch {
		case strings.HasPrefix(r, prefixSuffix):
			set[r[2:]] = struct{}{}
			set[r] = struct{}{}
		case strings.HasPrefix(r, prefixSubdomain):
			set["+"+r] = struct{}{}
		default:
			set[r] = struct{}{}
		}
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, reverseString(k))
	}
	sort.Strings(keys)
	return keys
}

// keysToRules undoes domainKeys. A "+.x" key without a matching "x" key
// can only have come from ".x" (subdomains only).
func keysToRules(keys []string) []string {
	have := make(map[string]bool, len(keys))
	for _, k := range keys {
		have[k] = true
	}
	out := make([]string, 0, len(keys))
	consumed := map[string]bool{}
	for _, k := range keys {
		if strings.HasPrefix(k, prefixSuffix) {
			base := k[2:]
			if have[base] {
				out = append(out, k)
				consumed[base] = true
			} else {
				out = append(out, prefixSubdomain+base)
			}
		}
	}
	for _, k := range keys {
		if !strings.HasPrefix(k, prefixSuffix) && !consumed[k] {
			out = append(out, k)
		}
	}
	return out
}

type domainSet struct {
	leaves, labelBitmap []uint64
	labels              []byte
}

func setBit(bm *[]uint64, i int, v int) {
	for i>>6 >= len(*bm) {
		*bm = append(*bm, 0)
	}
	(*bm)[i>>6] |= uint64(v) << uint(i&63)
}

func getBit(bm []uint64, i int) bool {
	w := i >> 6
	if w >= len(bm) {
		return false
	}
	return bm[w]&(1<<uint(i&63)) != 0
}

// buildDomainSet is mihomo's NewDomainSet: a breadth-first walk over sorted
// keys. It runs level by level so finished levels can be freed.
func buildDomainSet(keys []string) *domainSet {
	ss := &domainSet{}
	type qElt struct{ s, e, col int }
	level := []qElt{{0, len(keys), 0}}
	node, lIdx := 0, 0
	for len(level) > 0 {
		var next []qElt
		for _, elt := range level {
			if elt.col == len(keys[elt.s]) {
				elt.s++
				setBit(&ss.leaves, node, 1)
			}
			for j := elt.s; j < elt.e; {
				frm := j
				for ; j < elt.e && keys[j][elt.col] == keys[frm][elt.col]; j++ {
				}
				next = append(next, qElt{frm, j, elt.col + 1})
				ss.labels = append(ss.labels, keys[frm][elt.col])
				setBit(&ss.labelBitmap, lIdx, 0)
				lIdx++
			}
			setBit(&ss.labelBitmap, lIdx, 1)
			lIdx++
			node++
		}
		level = next
	}
	return ss
}

func readDomainSet(r *limitedReader) ([]string, error) {
	var ver [1]byte
	if _, err := io.ReadFull(r, ver[:]); err != nil {
		return nil, err
	}
	if ver[0] != 1 {
		return nil, fmt.Errorf("неподдерживаемая версия доменного набора %d", ver[0])
	}
	leaves, err := readWords(r)
	if err != nil {
		return nil, err
	}
	bitmap, err := readWords(r)
	if err != nil {
		return nil, err
	}
	n, err := readI64(r)
	if err != nil {
		return nil, err
	}
	if n < 1 || n > r.left {
		return nil, errors.New("повреждена длина меток")
	}
	labels := make([]byte, n)
	if _, err := io.ReadFull(r, labels); err != nil {
		return nil, err
	}

	// Every node but the root is reached by exactly one labelled edge, so a
	// trie with L labels has L+1 nodes and 2L+1 bitmap bits.
	L := len(labels)
	totalBits := 2*L + 1
	if len(bitmap)*64 < totalBits {
		return nil, errors.New("битовая карта короче, чем нужно")
	}
	firstEdge := make([]int32, 0, L+2)
	firstEdge = append(firstEdge, 0)
	edges := 0
	for i := 0; i < totalBits; i++ {
		if getBit(bitmap, i) {
			firstEdge = append(firstEdge, int32(edges))
		} else {
			edges++
			if edges > L {
				return nil, errors.New("структура дерева повреждена")
			}
		}
	}
	if edges != L || len(firstEdge) != L+2 {
		return nil, errors.New("структура дерева повреждена")
	}

	// Iterative depth-first walk collecting reversed keys.
	type frame struct{ node, e, end int32 }
	var keys []string
	key := make([]byte, 0, 64)
	stack := []frame{{0, firstEdge[0], firstEdge[1]}}
	for len(stack) > 0 {
		top := &stack[len(stack)-1]
		if top.e >= top.end {
			if top.node != 0 {
				key = key[:len(key)-1]
			}
			stack = stack[:len(stack)-1]
			continue
		}
		e := top.e
		top.e++
		child := e + 1
		key = append(key, labels[e])
		if getBit(leaves, int(child)) {
			keys = append(keys, reverseString(string(key)))
		}
		stack = append(stack, frame{child, firstEdge[child], firstEdge[child+1]})
	}
	if len(keys) == 0 {
		return nil, errors.New("набор не содержит доменов")
	}
	return keysToRules(keys), nil
}

// ---- ip ranges -------------------------------------------------------------

func readIPRanges(r *limitedReader) ([]netip.Prefix, error) {
	var ver [1]byte
	if _, err := io.ReadFull(r, ver[:]); err != nil {
		return nil, err
	}
	if ver[0] != 1 {
		return nil, fmt.Errorf("неподдерживаемая версия IP-набора %d", ver[0])
	}
	n, err := readI64(r)
	if err != nil {
		return nil, err
	}
	if n < 1 || n > r.left/32 {
		return nil, errors.New("повреждена длина списка диапазонов")
	}
	var out []netip.Prefix
	var buf [32]byte
	for i := int64(0); i < n; i++ {
		if _, err := io.ReadFull(r, buf[:]); err != nil {
			return nil, err
		}
		from := netip.AddrFrom16([16]byte(buf[:16])).Unmap()
		to := netip.AddrFrom16([16]byte(buf[16:])).Unmap()
		rng := netipx.IPRangeFrom(from, to)
		if !rng.IsValid() {
			return nil, errors.New("неверный диапазон адресов")
		}
		out = rng.AppendPrefixes(out)
	}
	return out, nil
}

// ---- helpers ---------------------------------------------------------------

type limitedReader struct {
	r    io.Reader
	left int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.left <= 0 {
		return 0, errors.New("распакованный файл слишком большой")
	}
	if int64(len(p)) > l.left {
		p = p[:l.left]
	}
	n, err := l.r.Read(p)
	l.left -= int64(n)
	return n, err
}

func writeI64(w *bytes.Buffer, v int64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(v))
	w.Write(b[:])
}

func readI64(r io.Reader) (int64, error) {
	var b [8]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	return int64(binary.BigEndian.Uint64(b[:])), nil
}

func writeWords(w *bytes.Buffer, words []uint64) {
	writeI64(w, int64(len(words)))
	var b [8]byte
	for _, v := range words {
		binary.BigEndian.PutUint64(b[:], v)
		w.Write(b[:])
	}
}

func readWords(r *limitedReader) ([]uint64, error) {
	n, err := readI64(r)
	if err != nil {
		return nil, err
	}
	if n < 1 || n > r.left/8 {
		return nil, errors.New("повреждена длина массива")
	}
	out := make([]uint64, n)
	var b [8]byte
	for i := range out {
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return nil, err
		}
		out[i] = binary.BigEndian.Uint64(b[:])
	}
	return out, nil
}

// reverseString reverses by rune, as mihomo's utils.Reverse does.
func reverseString(s string) string {
	if isASCII(s) {
		b := []byte(s)
		for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
			b[i], b[j] = b[j], b[i]
		}
		return string(b)
	}
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}
