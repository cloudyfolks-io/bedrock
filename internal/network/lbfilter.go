package network

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

const noRoutes = "0.0.0.0/32"

type filterEntry struct {
	prefix netip.Prefix
	ge, le int
}

func parseFilterEntry(text string) filterEntry {
	fields := strings.Fields(text)
	prefix := netip.MustParsePrefix(fields[0])
	entry := filterEntry{prefix: prefix, ge: -1, le: -1}
	for i := 1; i+1 < len(fields); i += 2 {
		bound, _ := strconv.Atoi(fields[i+1])
		if fields[i] == "ge" {
			entry.ge = bound
		} else {
			entry.le = bound
		}
	}
	switch {
	case entry.ge < 0 && entry.le < 0:
		entry.ge, entry.le = prefix.Bits(), prefix.Bits()
	case entry.ge < 0:
		entry.ge = prefix.Bits()
	case entry.le < 0:
		entry.le = prefix.Addr().BitLen()
	}
	return entry
}

func (e filterEntry) String() string {
	text := e.prefix.String()
	if e.ge > e.prefix.Bits() {
		text += fmt.Sprintf(" ge %d", e.ge)
	}
	if e.le > e.prefix.Bits() {
		text += fmt.Sprintf(" le %d", e.le)
	}
	return text
}

func covers(outer, inner netip.Prefix) bool {
	return outer.Bits() <= inner.Bits() && outer.Contains(inner.Addr())
}

func sibling(half netip.Prefix) netip.Prefix {
	bytes := half.Addr().AsSlice()
	bit := half.Bits() - 1
	bytes[bit/8] ^= 1 << (7 - bit%8)
	addr, _ := netip.AddrFromSlice(bytes)
	return netip.PrefixFrom(addr, half.Bits())
}

func splitAround(entry filterEntry, hole netip.Prefix) []filterEntry {
	if covers(hole, entry.prefix) {
		return nil
	}
	var pieces []filterEntry
	for bits := entry.prefix.Bits() + 1; bits <= hole.Bits(); bits++ {
		piece := filterEntry{prefix: sibling(netip.PrefixFrom(hole.Addr(), bits).Masked()), ge: max(entry.ge, bits), le: entry.le}
		if piece.ge <= piece.le {
			pieces = append(pieces, piece)
		}
	}
	return pieces
}

func FilterWithout(filter []string, hole netip.Prefix) []string {
	if len(filter) == 0 {
		return []string{noRoutes}
	}
	var kept []string
	for _, text := range filter {
		entry := parseFilterEntry(text)
		if !entry.prefix.Overlaps(hole) {
			kept = append(kept, text)
			continue
		}
		for _, piece := range splitAround(entry, hole) {
			kept = append(kept, piece.String())
		}
	}
	if len(kept) == 0 {
		return []string{noRoutes}
	}
	return kept
}
