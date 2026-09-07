// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package library

import (
	"context"
	"crypto/md5" //nolint:gosec // the OpenSubsonic auth scheme mandates MD5; see below
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/andrewloable/jockora/internal/store"
)

// Subsonic reads a library from an OpenSubsonic server: Navidrome, Airsonic,
// Gonic and others speak it.
//
// WHY THIS MATTERS MORE THAN A SECOND FILE FORMAT: the target listener ALREADY
// RUNS ONE OF THESE. Pointing Jockora at the Navidrome they have removes the
// whole "give it a folder and wait for a scan" step and matches how their music
// is already organised.
//
// Tracks are stored with their STREAM URL where a local track stores its path.
// Nothing downstream changes: ffmpeg takes a URL for -i exactly as it takes a
// filename, so decoding, loudness measurement and playback are untouched.
type Subsonic struct {
	BaseURL  string
	User     string
	Password string
	Client   *http.Client

	// SourceID stamps the rows and namespaces their paths.
	SourceID int64

	// Stamp marks every row this walk sees. Zero means the clock.
	Stamp int64

	// PageSize overrides how many albums are requested at a time. Zero means
	// pageSize. It exists so paging can actually be TESTED: with the real value
	// a test would need 500 fake albums before the loop fetched a second page,
	// and a paging bug that only appears on large libraries is exactly the one
	// nobody finds until someone has a large library.
	PageSize int
}

func (s Subsonic) page() int {
	if s.PageSize > 0 {
		return s.PageSize
	}
	return pageSize
}

func (s Subsonic) Name() string { return "opensubsonic " + s.BaseURL }

// apiVersion is the oldest version carrying everything used here, so the
// widest range of servers is accepted.
const apiVersion = "1.16.1"

// pageSize is how many albums are requested at a time. 500 is the maximum the
// specification allows.
const pageSize = 500

func (s Subsonic) httpClient() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// auth builds the query parameters every request carries.
//
// The salted-MD5 token is what the OpenSubsonic specification requires; it is a
// PROTOCOL requirement, not a security choice made here, and MD5 is used for no
// other purpose in this program. The password never travels in the URL.
func (s Subsonic) auth() (url.Values, error) {
	salt := make([]byte, 8)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("library: generating salt: %w", err)
	}
	hexSalt := hex.EncodeToString(salt)
	sum := md5.Sum([]byte(s.Password + hexSalt)) //nolint:gosec // protocol-mandated

	return url.Values{
		"u": {s.User},
		"t": {hex.EncodeToString(sum[:])},
		"s": {hexSalt},
		"v": {apiVersion},
		"c": {"jockora"},
		"f": {"json"},
	}, nil
}

// subsonicResponse is the envelope every endpoint wraps its payload in.
type subsonicResponse struct {
	SR struct {
		Status string `json:"status"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		AlbumList2 struct {
			Album []struct {
				ID string `json:"id"`
			} `json:"album"`
		} `json:"albumList2"`
		Album struct {
			Song []subsonicSong `json:"song"`
		} `json:"album"`
	} `json:"subsonic-response"`
}

type subsonicSong struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Artist   string `json:"artist"`
	Album    string `json:"album"`
	Year     int    `json:"year"`
	Duration int    `json:"duration"`
	Size     int64  `json:"size"`
	Suffix   string `json:"suffix"`
}

func (s Subsonic) get(ctx context.Context, endpoint string, extra url.Values) (*subsonicResponse, error) {
	q, err := s.auth()
	if err != nil {
		return nil, err
	}
	for k, vs := range extra {
		for _, v := range vs {
			q.Add(k, v)
		}
	}

	u := strings.TrimSuffix(s.BaseURL, "/") + "/rest/" + endpoint + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("library: %s: %w", endpoint, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("library: %s: HTTP %s", endpoint, resp.Status)
	}
	var out subsonicResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("library: %s: decoding: %w", endpoint, err)
	}
	// A Subsonic server reports failure INSIDE a 200. Treating HTTP status as
	// the answer would read "wrong password" as a successful empty library and
	// silently produce a station with no music.
	if e := out.SR.Error; e != nil {
		return nil, fmt.Errorf("library: %s: server error %d: %s", endpoint, e.Code, e.Message)
	}
	if out.SR.Status != "" && out.SR.Status != "ok" {
		return nil, fmt.Errorf("library: %s: server status %q", endpoint, out.SR.Status)
	}
	return &out, nil
}

// Ping checks credentials and reachability before a scan commits to anything.
func (s Subsonic) Ping(ctx context.Context) error {
	_, err := s.get(ctx, "ping.view", nil)
	return err
}

// StreamURL is what gets stored where a local track stores its path.
//
// It carries its own credentials, because ffmpeg fetches it as an ordinary HTTP
// request with no knowledge of this package.
//
// THE SALT IS DERIVED FROM THE TRACK ID, NOT RANDOM, and that is load-bearing
// rather than a shortcut. upsert keys rows on the path, so a random salt would
// make every rescan produce a different URL for the same song and DUPLICATE THE
// ENTIRE LIBRARY on each scan -- ten thousand tracks becoming twenty. A
// deterministic salt makes the URL stable, which makes the scan idempotent,
// which is the contract Source.Scan promises.
//
// The tradeoff, stated rather than buried: a long-lived auth token for this
// account is written into the local database. That is the same trust boundary
// as the password already sitting in the operator's config, and the salt being
// per-track rather than per-request costs nothing extra, because the token is
// stored persistently either way.
func (s Subsonic) StreamURL(id string) (string, error) {
	q := s.stableAuth(id)
	q.Set("id", id)
	return strings.TrimSuffix(s.BaseURL, "/") + "/rest/stream.view?" + q.Encode(), nil
}

// stableAuth is auth() with the salt derived from a key instead of drawn at
// random, so the same key always yields the same URL.
func (s Subsonic) stableAuth(key string) url.Values {
	saltSum := md5.Sum([]byte("jockora-salt:" + s.User + ":" + key)) //nolint:gosec // protocol-mandated
	hexSalt := hex.EncodeToString(saltSum[:])[:16]
	sum := md5.Sum([]byte(s.Password + hexSalt)) //nolint:gosec // protocol-mandated

	return url.Values{
		"u": {s.User},
		"t": {hex.EncodeToString(sum[:])},
		"s": {hexSalt},
		"v": {apiVersion},
		"c": {"jockora"},
		"f": {"json"},
	}
}

// Scan walks every album and records its songs.
//
// Album by album rather than one enormous search, because that is the only
// enumeration every OpenSubsonic server implements the same way, and it pages
// predictably on libraries too large to hold in one response.
func (s Subsonic) Scan(ctx context.Context, st *store.Store, progress ScanProgress) (Stats, error) {
	var stats Stats

	if err := s.Ping(ctx); err != nil {
		return stats, err
	}

	size := s.page()
	for offset := 0; ; offset += size {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		page, err := s.get(ctx, "getAlbumList2.view", url.Values{
			"type":   {"alphabeticalByName"},
			"size":   {strconv.Itoa(size)},
			"offset": {strconv.Itoa(offset)},
		})
		if err != nil {
			return stats, err
		}
		albums := page.SR.AlbumList2.Album
		if len(albums) == 0 {
			break
		}

		for _, a := range albums {
			detail, err := s.get(ctx, "getAlbum.view", url.Values{"id": {a.ID}})
			if err != nil {
				// One unreadable album must not end a scan of ten thousand.
				stats.Unplayable++
				stats.Rejected = append(stats.Rejected, err)
				continue
			}
			for _, song := range detail.SR.Album.Song {
				stats.Found++
				if progress != nil {
					progress(stats.Found, song.Artist+" - "+song.Title)
				}

				added, err := upsert(ctx, st, SubsonicLocator(s.SourceID, song.ID), metadata{
					artist:   song.Artist,
					title:    song.Title,
					album:    song.Album,
					year:     song.Year,
					duration: float64(song.Duration),
				}, true, song.Size, 0, s.SourceID, orNow(s.Stamp))
				if err != nil {
					return stats, err
				}
				if added {
					stats.Added++
				} else {
					stats.Updated++
				}
			}
		}
		if len(albums) < size {
			break
		}
	}
	return stats, nil
}

var _ Source = Subsonic{}
