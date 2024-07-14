package duckduckgo

import (
	"encoding/json"
	"fmt"
	"html"
	"math"
	"net/url"
	"regexp"
	"strings"

	lru "github.com/hashicorp/golang-lru/v2"
)

var (
	regex500InURL      = regexp.MustCompile(`(?:\d{3}-\d{2}\.js)`)
	regexStripTags     = regexp.MustCompile("<.*?>")
	orjsonAvailable    = false
	is500InURLCache, _ = lru.New[string, bool](1000)
)

func rangeFunc(start, end, step int) []int {
	var result []int
	for i := start; i < end; i = i + step {
		result = append(result, i)
	}
	return result
}

/**
 * Extracts the VQD from the HTML.
**/
func extractVQD(htmlBytes []byte, keywords string) (string, error) {
	candidates := [][]byte{
		[]byte(`vqd="`), []byte(`"`),
		[]byte(`vqd=`), []byte(`&`),
		[]byte(`vqd='`), []byte(`'`),
	}
	for i := 0; i < len(candidates); i += 2 {
		c1 := candidates[i]
		c2 := candidates[i+1]
		start := strings.Index(string(htmlBytes), string(c1)) + len(c1)
		if start >= len(c1) {
			end := strings.Index(string(htmlBytes[start:]), string(c2))
			if end > 0 {
				return string(htmlBytes[start : start+end]), nil
			}
		}
	}
	return "", fmt.Errorf("extractVQD() keywords=%s Could not extract vqd.", keywords)
}

/**
 * text(backend="api") -> extract json from html.
**/
func textExtractJSON(htmlBytes []byte, keywords string) ([]map[string]any, error) {
	start := strings.Index(string(htmlBytes), "DDG.pageLayout.load('d',") + 24
	end := strings.Index(string(htmlBytes[start:]), ");DDG.duckbar.load(")
	if start < 0 || end < 0 {
		return nil, fmt.Errorf("textExtractJSON() keywords=%s return None", keywords)
	}
	data := htmlBytes[start : start+end]
	var result []map[string]any
	err := json.Unmarshal(data, &result)
	if err != nil {
		return nil, fmt.Errorf("textExtractJSON() keywords=%s %s", keywords, err)
	}
	return result, nil
}

/**
* Something like '506-00.js' inside the url.
**/
func is500InURL(url string) bool {
	exists, result := is500InURLCache.ContainsOrAdd(url, true)
	if exists {
		return result
	}
	result = regex500InURL.MatchString(url)
	is500InURLCache.Add(url, result)
	return result
}

/**
* Strip HTML tags from the raw_html string.
**/
func normalize(rawHTML string) string {
	if rawHTML == "" {
		return ""
	}
	return html.UnescapeString(regexStripTags.ReplaceAllString(rawHTML, ""))
}

/**
* Unquote URL and replace spaces with '+'.
**/
func normalizeURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	normalizedURL, err := url.QueryUnescape(strings.ReplaceAll(rawURL, " ", "+"))
	if err != nil {
		return rawURL // Return the original URL if there's an error
	}
	return normalizedURL
}

/**
* Calculate distance between two points in km. Haversine formula.
**/
func calculateDistance(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371.0087714 // Earth radius in kilometers
	rlat1, rlon1, rlat2, rlon2 := math.Pi*lat1/180, math.Pi*lon1/180, math.Pi*lat2/180, math.Pi*lon2/180
	dlon, dlat := rlon2-rlon1, rlat2-rlat1
	a := math.Pow(math.Sin(dlat/2), 2) + math.Cos(rlat1)*math.Cos(rlat2)*math.Pow(math.Sin(dlon/2), 2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return R * c
}
