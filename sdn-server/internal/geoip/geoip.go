// Package geoip is a fail-open connector over a MaxMind GeoLite2-City .mmdb
// database. It resolves a public IP to coarse coordinates for the node status
// dashboard. It is deliberately best-effort: when the database is absent or a
// lookup misses, callers get a zero Location and no error, so the status feed
// keeps working on a node that never provisioned an mmdb.
package geoip

import (
	"net"
	"strings"
	"sync"

	logging "github.com/ipfs/go-log/v2"
	maxminddb "github.com/oschwald/maxminddb-golang"
)

var log = logging.Logger("sdn-geoip")

// Location is the resolved geo data for an IP. All fields are zero when the IP
// could not be resolved.
type Location struct {
	Lat     float32
	Lon     float32
	Country string
	City    string
}

// Reader is a concurrency-safe handle over an open GeoLite2-City database.
// The zero value (and a nil *Reader) is valid and always resolves to an empty
// Location — this is the fail-open path when no mmdb is configured or present.
type Reader struct {
	mu sync.RWMutex
	db *maxminddb.Reader
}

// cityRecord mirrors the subset of the GeoLite2-City record layout the
// dashboard needs. Tags match the on-disk MaxMind field names.
type cityRecord struct {
	Location struct {
		Latitude  float64 `maxminddb:"latitude"`
		Longitude float64 `maxminddb:"longitude"`
	} `maxminddb:"location"`
	Country struct {
		ISOCode string            `maxminddb:"iso_code"`
		Names   map[string]string `maxminddb:"names"`
	} `maxminddb:"country"`
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
}

// Open opens the GeoLite2-City database at path. It never returns an error:
// an empty or missing path, or an unreadable/invalid file, yields a fail-open
// Reader that resolves every IP to an empty Location. The single boot-time log
// line records which state the node started in.
func Open(path string) *Reader {
	r := &Reader{}
	path = strings.TrimSpace(path)
	if path == "" {
		log.Info("GeoIP disabled: no database path configured")
		return r
	}
	db, err := maxminddb.Open(path)
	if err != nil {
		// Fail open: this is expected on nodes that never provisioned an
		// mmdb. One line at boot, then silence — no per-lookup spam.
		log.Infof("GeoIP disabled: %s unavailable (%v)", path, err)
		return r
	}
	r.db = db
	log.Infof("GeoIP enabled: %s", path)
	return r
}

// Lookup resolves ip (a textual address, IPv4 or IPv6) to a Location. It
// fail-opens to an empty Location for a nil reader, an unconfigured database,
// an unparseable IP, a miss, or a decode error.
func (r *Reader) Lookup(ip string) Location {
	if r == nil {
		return Location{}
	}
	r.mu.RLock()
	db := r.db
	r.mu.RUnlock()
	if db == nil {
		return Location{}
	}
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return Location{}
	}
	var rec cityRecord
	if err := db.Lookup(parsed, &rec); err != nil {
		return Location{}
	}
	loc := Location{
		Lat:  float32(rec.Location.Latitude),
		Lon:  float32(rec.Location.Longitude),
		City: rec.City.Names["en"],
	}
	if name := rec.Country.Names["en"]; name != "" {
		loc.Country = name
	} else {
		loc.Country = rec.Country.ISOCode
	}
	return loc
}

// Close releases the underlying database, if any. Safe on a nil or fail-open
// Reader.
func (r *Reader) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.db == nil {
		return nil
	}
	err := r.db.Close()
	r.db = nil
	return err
}
