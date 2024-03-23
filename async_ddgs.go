package duckduckgo

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/antchfx/htmlquery"
	"github.com/samber/lo"
	"golang.org/x/net/html"
)

/**
* DuckDuckgo_search async class to get search results from duckduckgo.com.
**/
type AsyncDDGS struct {
	Executor *http.Client
	Proxies  map[string]string
	Timeout  int
}

func NewAsyncDDGS(headers map[string]string, proxies map[string]string, timeout int) *AsyncDDGS {
	client := &http.Client{
		Timeout: time.Duration(timeout) * time.Second,
	}
	return &AsyncDDGS{
		Executor: client,
		Proxies:  proxies,
		Timeout:  timeout,
	}
}

func (a *AsyncDDGS) getExecutor() *http.Client {
	if a.Executor == nil {
		a.Executor = &http.Client{}
	}
	return a.Executor
}

func (a *AsyncDDGS) agetURL(method string, url string, data map[string]string, params map[string]string) ([]byte, error) {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	for key, value := range params {
		q.Add(key, value)
	}
	req.URL.RawQuery = q.Encode()
	resp, err := a.Executor.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respContent, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return respContent, nil
}

func (a *AsyncDDGS) agetVqd(keywords string) (string, error) {
	respContent, err := a.agetURL("POST", "https://duckduckgo.com", nil, map[string]string{"q": keywords})
	if err != nil {
		return "", err
	}
	return extractVQD(respContent, keywords)
}

/**
*	region: wt-wt, us-en, uk-en, ru-ru, etc. Defaults to "wt-wt".
*   timelimit: d, w, m, y. Defaults to None.
*	safesearch: "moderate", "off", "on". Defaults to "moderate".
**/
func (a *AsyncDDGS) Text(keywords string, region string, safesearch string, timelimit string, backend string, maxResults int) ([]map[string]string, error) {
	if region == "" {
		region = "wt-wt"
	}
	if safesearch == "" {
		safesearch = "moderate"
	}
	if backend == "" {
		backend = "api"
	}

	if backend == "api" {
		results, err := a.textAPI(keywords, region, safesearch, timelimit, maxResults)
		if err != nil {
			return nil, err
		}
		return results, nil
	} else if backend == "html" {
		results, err := a.textHTML(keywords, region, safesearch, timelimit, maxResults)
		if err != nil {
			return nil, err
		}
		return results, nil
	} else if backend == "lite" {
		results, err := a.textLite(keywords, region, timelimit, maxResults)
		if err != nil {
			return nil, err
		}
		return results, nil
	}
	return nil, fmt.Errorf("Invalid backend")
}

func (a *AsyncDDGS) textAPI(keywords string, region string, safesearch string, timelimit string, maxResults int) ([]map[string]string, error) {
	if keywords == "" {
		return nil, fmt.Errorf("Keywords is mandatory")
	}
	if region == "" {
		region = "wt-wt"
	}
	if safesearch == "" {
		safesearch = "moderate"
	}

	vqd, err := a.agetVqd(keywords)
	if err != nil {
		return nil, err
	}
	payload := map[string]string{
		"q":           keywords,
		"kl":          region,
		"l":           region,
		"vqd":         vqd,
		"bing_market": region,
		"a":           "ftsa",
	}
	safesearch = strings.ToLower(safesearch)
	if safesearch == "moderate" {
		payload["ex"] = "-1"
	} else if safesearch == "off" {
		payload["ex"] = "-2"
	} else if safesearch == "on" { // strict
		payload["p"] = "1"
	}
	if timelimit != "" {
		payload["df"] = timelimit
	}

	cache := make(map[string]bool)
	results := make([]map[string]string, 1100)

	var wg sync.WaitGroup
	var mu sync.Mutex
	textAPIPage := func(s int, page int) {
		defer wg.Done()
		priority := page * 100
		payload["s"] = fmt.Sprintf("%d", s)
		respContent, err := a.agetURL("GET", "https://links.duckduckgo.com/d.js", nil, payload)
		if err != nil {
			return
		}
		pageData, err := textExtractJSON(respContent, keywords)
		if err != nil {
			return
		}

		for _, row := range pageData {
			href, exist := row["u"].(string)
			if !exist {
				continue
			}
			if href != "" && !cache[href] && href != fmt.Sprintf("http://www.google.com/search?q=%s", keywords) {
				cache[href] = true
				body := normalize(row["a"].(string))
				if body != "" {
					priority++
					result := map[string]string{
						"title": normalize(row["t"].(string)),
						"href":  normalizeURL(href),
						"body":  body,
					}
					mu.Lock()
					results[priority] = result
					mu.Unlock()
				}
			}
		}
	}
	wg.Add(1)
	go textAPIPage(0, 0)
	if maxResults > 0 {
		maxResults = lo.Min([]int{maxResults, 500})
		for i, s := range rangeFunc(23, maxResults, 50) {
			wg.Add(1)
			go textAPIPage(s, i+1)
		}
	}
	wg.Wait()

	return lo.Slice(lo.Filter(results, func(res map[string]string, _ int) bool {
		return res != nil
	}), 0, maxResults), nil
}

func (a *AsyncDDGS) textHTML(keywords string, region string, safesearch string, timelimit string, maxResults int) ([]map[string]string, error) {
	if keywords == "" {
		return nil, fmt.Errorf("Keywords is mandatory")
	}
	a.Executor.Timeout = time.Duration(a.Timeout) * time.Second
	safesearchBase := map[string]string{
		"on":       "1",
		"moderate": "-1",
		"off":      "-2",
	}
	payload := map[string]string{
		"q":   keywords,
		"kl":  region,
		"p":   safesearchBase[strings.ToLower(safesearch)],
		"o":   "json",
		"api": "d.js",
	}
	if timelimit != "" {
		payload["df"] = timelimit
	}
	if maxResults > 20 {
		vqd, err := a.agetVqd(keywords)
		if err != nil {
			return nil, err
		}
		payload["vqd"] = vqd
	}
	cache := make(map[string]bool)
	results := make([]map[string]string, 1100)
	var wg sync.WaitGroup
	var mu sync.Mutex
	textHTMLPage := func(s int, page int) {
		defer wg.Done()
		priority := page * 100
		payload["s"] = fmt.Sprintf("%d", s)
		respContent, err := a.agetURL("POST", "https://html.duckduckgo.com/html", nil, payload)
		if err != nil {
			return
		}
		if bytes.Contains(respContent, []byte("No  results.")) {
			return
		}
		tree, err := htmlquery.Parse(bytes.NewReader(respContent))
		if err != nil {
			return
		}
		for _, e := range htmlquery.Find(tree, "//div[h2]") {
			href := htmlquery.FindOne(e, "./a/@href")
			if href != nil {
				href := htmlquery.InnerText(href)
				if href != "" && !cache[href] && !strings.HasPrefix(href, "http://www.google.com/search?q=") && !strings.HasPrefix(href, "https://duckduckgo.com/y.js?ad_domain") {
					cache[href] = true
					title := htmlquery.FindOne(e, "./h2/a/text()")
					body := htmlquery.Find(e, "./a//text()")
					priority++
					result := map[string]string{
						"title": normalize(htmlquery.InnerText(title)),
						"href":  normalizeURL(href),
						"body":  normalize(strings.Join(lo.Map(body, func(d *html.Node, _ int) string { return d.Data }), "")),
					}
					mu.Lock()
					results[priority] = result
					mu.Unlock()
				}
			}
		}
	}
	wg.Add(1)
	go textHTMLPage(0, 0)
	if maxResults > 0 {
		maxResults = lo.Min([]int{maxResults, 500})
		for i, s := range rangeFunc(23, maxResults, 50) {
			wg.Add(1)
			go textHTMLPage(s, i+1)
		}
	}
	wg.Wait()
	return lo.Slice(lo.Filter(results, func(res map[string]string, _ int) bool {
		return res != nil
	}), maxResults, 0), nil
}

func (a *AsyncDDGS) textLite(keywords string, region string, timelimit string, maxResults int) ([]map[string]string, error) {
	if keywords == "" {
		return nil, fmt.Errorf("Keywords is mandatory")
	}
	a.Executor.Timeout = time.Duration(a.Timeout) * time.Second
	payload := map[string]string{
		"q":   keywords,
		"o":   "json",
		"api": "d.js",
		"kl":  region,
	}
	if timelimit != "" {
		payload["df"] = timelimit
	}
	cache := make(map[string]bool)
	results := make([]map[string]string, 1100)
	var wg sync.WaitGroup
	var mu sync.Mutex
	textLitePage := func(s int, page int) {
		defer wg.Done()
		priority := page * 100
		payload["s"] = fmt.Sprintf("%d", s)
		respContent, err := a.agetURL("POST", "https://lite.duckduckgo.com/lite/", nil, payload)
		if err != nil {
			return
		}
		if bytes.Contains(respContent, []byte("No more results.")) {
			return
		}
		tree, err := htmlquery.Parse(bytes.NewReader(respContent))
		if err != nil {
			return
		}
		var title string
		var body string
		var href string
		data := htmlquery.Find(tree, "//table[last()]//tr")
		for i, e := range data {
			if i == 0 {
				htmlHref := htmlquery.FindOne(e, ".//a//@href")
				if htmlHref != nil {
					href = htmlquery.InnerText(htmlHref)
					if href == "" || cache[href] || strings.HasPrefix(href, "http://www.google.com/search?q=") || strings.HasPrefix(href, "https://duckduckgo.com/y.js?ad_domain") {
						for range [3]struct{}{} { // skip block(i=1,2,3,4)
							_, _ = next(data, nil)
						}
					} else {
						cache[href] = true
						title = htmlquery.InnerText(htmlquery.FindOne(e, ".//a//text()"))
					}
				}
			} else if i == 1 {
				htmlbody := htmlquery.Find(e, ".//td[@class='result-snippet']//text()")
				body = strings.Join(lo.Map(htmlbody, func(d *html.Node, _ int) string { return d.Data }), "")
			} else if i == 2 {
				priority++
				result := map[string]string{
					"title": normalize(title),
					"href":  normalizeURL(href),
					"body":  normalize(body),
				}
				mu.Lock()
				results[priority] = result
				mu.Unlock()
			}
		}
	}
	wg.Add(1)
	go textLitePage(0, 0)
	if maxResults > 0 {
		maxResults = lo.Min([]int{maxResults, 500})
		for i, s := range rangeFunc(23, maxResults, 50) {
			wg.Add(1)
			go textLitePage(s, i+1)
		}
	}
	wg.Wait()
	return lo.Slice(lo.Filter(results, func(res map[string]string, _ int) bool {
		return res != nil
	}), maxResults, 0), nil
}

func next(it []*html.Node, def *struct{}) (any, bool) {
	if len(it) == 0 {
		return def, false
	}
	v := (it)[0]
	it = (it)[1:]
	return &v, true
}
