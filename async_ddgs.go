package duckduckgo

import (
	"bytes"
	"encoding/json"
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

func (a *AsyncDDGS) agetURL(method string, url string, data []byte, params map[string]string) ([]byte, error) {
	req, err := http.NewRequest(method, url, bytes.NewBuffer(data))
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

/*
*
  - region: wt-wt, us-en, uk-en, ru-ru, etc. Defaults to "wt-wt".
  - safesearch: on, moderate, off. Defaults to "moderate".
  - timelimit: Day, Week, Month, Year. Defaults to None.
  - size: Small, Medium, Large, Wallpaper. Defaults to None.
  - color: color, Monochrome, Red, Orange, Yellow, Green, Blue,
    Purple, Pink, Brown, Black, Gray, Teal, White. Defaults to None.
  - type_image: photo, clipart, gif, transparent, line.
    Defaults to None.
  - layout: Square, Tall, Wide. Defaults to None.

*
*/
func (a *AsyncDDGS) Images(keywords string, region string, safesearch string, timelimit string, size string, color string, typeImage string, layout string, licenseImage string, maxResults int) ([]map[string]string, error) {
	if keywords == "" {
		return nil, fmt.Errorf("keywords is mandatory")
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

	safesearchBase := map[string]string{
		"on":       "1",
		"moderate": "1",
		"off":      "-1",
	}

	payload := map[string]string{
		"l":   region,
		"o":   "json",
		"q":   keywords,
		"vqd": vqd,
		"p":   safesearchBase[strings.ToLower(safesearch)],
	}

	f := ""
	if len(timelimit) > 0 {
		f += "time:" + timelimit + ","
	}
	if len(size) > 0 {
		f += "size:" + size + ","
	}
	if len(color) > 0 {
		f += "color:" + color + ","
	}
	if len(typeImage) > 0 {
		f += "type:" + typeImage + ","
	}
	if len(layout) > 0 {
		f += "layout:" + layout + ","
	}
	if len(licenseImage) > 0 {
		f += "license:" + licenseImage
	}

	if len(f) > 0 {
		payload["f"] = f

	}

	cache := make(map[string]bool)
	results := make([]map[string]string, 600)

	var wg sync.WaitGroup
	var mu sync.Mutex
	imagesPage := func(s, page int) {
		defer wg.Done()
		priority := page * 100
		payload["s"] = fmt.Sprintf("%d", s)
		respContent, err := a.agetURL("GET", "https://duckduckgo.com/i.js", nil, payload)
		if err != nil {
			return
		}
		var respJSON map[string]interface{}
		err = json.Unmarshal(respContent, &respJSON)
		if err != nil {
			return
		}
		pageData, ok := respJSON["results"].([]interface{})
		if !ok {
			return
		}

		for _, row := range pageData {
			rowMap, ok := row.(map[string]interface{})
			if !ok {
				continue
			}

			imageURL, ok := rowMap["image"].(string)
			if !ok || imageURL == "" {
				continue
			}

			if _, ok := cache[imageURL]; !ok {
				cache[imageURL] = true
				priority++
				result := map[string]string{
					"title":     rowMap["title"].(string),
					"image":     normalizeURL(imageURL),
					"thumbnail": normalizeURL(rowMap["thumbnail"].(string)),
					"url":       normalizeURL(rowMap["url"].(string)),
					"height":    fmt.Sprintf("%v", rowMap["height"]),
					"width":     fmt.Sprintf("%v", rowMap["width"]),
					"source":    rowMap["source"].(string),
				}
				mu.Lock()
				results[priority] = result
				mu.Unlock()
			}
		}
	}

	wg.Add(1)
	go imagesPage(0, 0)
	if maxResults > 0 {
		maxResults = lo.Min([]int{maxResults, 500})
		for i, s := range rangeFunc(100, maxResults, 100) {
			wg.Add(1)
			go imagesPage(s, i+1)
		}
	}
	wg.Wait()

	return lo.Slice(lo.Filter(results, func(res map[string]string, _ int) bool {
		return res != nil
	}), 0, maxResults), nil
}

// region: wt-wt, us-en, uk-en, ru-ru, etc. Defaults to "wt-wt".
// safesearch: on, moderate, off. Defaults to "moderate".
// timelimit: d, w, m. Defaults to None.
// resolution: high, standart. Defaults to None.
// duration: short, medium, long. Defaults to None.
// license_videos: creativeCommon, youtube. Defaults to None.
func (a *AsyncDDGS) Videos(keywords string, region string, safesearch string, timelimit string, resolution string, duration string, licenseVideos string, maxResults int) ([]map[string]interface{}, error) {
	if keywords == "" {
		return nil, fmt.Errorf("keywords is mandatory")
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

	safesearchBase := map[string]string{
		"on":       "1",
		"moderate": "-1",
		"off":      "-2",
	}

	payload := map[string]string{
		"l":   region,
		"o":   "json",
		"q":   keywords,
		"vqd": vqd,
		"p":   safesearchBase[strings.ToLower(safesearch)],
	}

	f := ""
	if len(timelimit) > 0 {
		f += "publishedAfter:" + timelimit + ","
	}
	if len(resolution) > 0 {
		f += "videoDefinition:" + resolution + ","
	}
	if len(duration) > 0 {
		f += "videoDuration:" + duration + ","
	}
	if len(licenseVideos) > 0 {
		f += "videoLicense:" + licenseVideos + ","
	}

	if len(f) > 0 {
		payload["f"] = f

	}

	cache := make(map[string]bool)
	results := make([]map[string]interface{}, 700)

	var wg sync.WaitGroup
	var mu sync.Mutex
	videosPage := func(s, page int) {
		defer wg.Done()
		priority := page * 100
		payload["s"] = fmt.Sprintf("%d", s)
		respContent, err := a.agetURL("GET", "https://duckduckgo.com/v.js", nil, payload)
		if err != nil {
			return
		}
		var respJSON map[string]interface{}
		err = json.Unmarshal(respContent, &respJSON)
		if err != nil {
			return
		}
		pageData, ok := respJSON["results"].([]interface{})
		if !ok {
			return
		}

		for _, row := range pageData {
			rowMap, ok := row.(map[string]interface{})
			if !ok {
				continue
			}

			content, ok := rowMap["content"].(string)
			if !ok || content == "" {
				continue
			}

			if _, ok := cache[content]; !ok {
				cache[content] = true
				priority++
				mu.Lock()
				results[priority] = rowMap
				mu.Unlock()
			}
		}
	}

	wg.Add(1)
	go videosPage(0, 0)
	if maxResults > 0 {
		maxResults = lo.Min([]int{maxResults, 400})
		for i, s := range rangeFunc(59, maxResults, 59) {
			wg.Add(1)
			go videosPage(s, i+1)
		}
	}
	wg.Wait()

	return lo.Slice(lo.Filter(results, func(res map[string]interface{}, _ int) bool {
		return res != nil
	}), 0, maxResults), nil
}

/*
*
  - region: wt-wt, us-en, uk-en, ru-ru, etc. Defaults to "wt-wt".
    safesearch: on, moderate, off. Defaults to "moderate".
    timelimit: d, w, m. Defaults to None.
*/
func (a *AsyncDDGS) News(keywords string, region string, safesearch string, timelimit string, maxResults int) ([]map[string]string, error) {
	if keywords == "" {
		return nil, fmt.Errorf("keywords is mandatory")
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

	safesearchBase := map[string]string{
		"on":       "1",
		"moderate": "-1",
		"off":      "-2",
	}

	payload := map[string]string{
		"l":     region,
		"o":     "json",
		"noamp": "1",
		"q":     keywords,
		"vqd":   vqd,
		"p":     safesearchBase[strings.ToLower(safesearch)],
	}

	if timelimit != "" {
		payload["df"] = timelimit
	}

	cache := make(map[string]bool)
	results := make([]map[string]string, 700)

	var wg sync.WaitGroup
	var mu sync.Mutex
	newsPage := func(s, page int) {
		defer wg.Done()
		priority := page * 100
		payload["s"] = fmt.Sprintf("%d", s)
		respContent, err := a.agetURL("GET", "https://duckduckgo.com/news.js", nil, payload)
		if err != nil {
			return
		}
		var respJSON map[string]interface{}
		err = json.Unmarshal(respContent, &respJSON)
		if err != nil {
			return
		}
		pageData, ok := respJSON["results"].([]interface{})
		if !ok {
			return
		}

		for _, row := range pageData {
			rowMap, ok := row.(map[string]interface{})
			if !ok {
				continue
			}

			url, ok := rowMap["url"].(string)
			if !ok || url == "" {
				continue
			}

			if _, ok := cache[url]; !ok {
				cache[url] = true
				imageURL, _ := rowMap["image"].(string)
				priority++
				result := map[string]string{
					"date":   time.Unix(int64(rowMap["date"].(float64)), 0).UTC().Format(time.RFC3339),
					"title":  rowMap["title"].(string),
					"body":   normalize(rowMap["excerpt"].(string)),
					"url":    normalizeURL(url),
					"image":  normalizeURL(imageURL),
					"source": rowMap["source"].(string),
				}
				mu.Lock()
				results[priority] = result
				mu.Unlock()
			}
		}
	}
	wg.Add(1)
	go newsPage(0, 0)
	if maxResults > 0 {
		maxResults = lo.Min([]int{maxResults, 400})
		for i, s := range rangeFunc(59, maxResults, 59) {
			wg.Add(1)
			go newsPage(s, i+1)
		}
	}
	wg.Wait()

	return lo.Slice(lo.Filter(results, func(res map[string]string, _ int) bool {
		return res != nil
	}), 0, maxResults), nil
}

func (a *AsyncDDGS) Answers(keywords string) ([]map[string]string, error) {
	if keywords == "" {
		return nil, fmt.Errorf("keywords is mandatory")
	}

	payload := map[string]string{
		"format": "json",
		"q":      fmt.Sprintf("what is %s", keywords),
	}

	respContent, err := a.agetURL("GET", "https://api.duckduckgo.com/", nil, payload)
	if err != nil {
		return nil, err
	}

	var pageData map[string]interface{}
	err = json.Unmarshal(respContent, &pageData)
	if err != nil {
		return nil, err
	}

	results := []map[string]string{}
	answer, _ := pageData["AbstractText"].(string)
	url, _ := pageData["AbstractURL"].(string)
	if answer != "" {
		results = append(results, map[string]string{
			"icon":  "",
			"text":  answer,
			"topic": "",
			"url":   url,
		})
	}

	payload["q"] = keywords

	respContent, err = a.agetURL("GET", "https://api.duckduckgo.com/", nil, payload)
	if err != nil {
		return nil, err
	}

	err = json.Unmarshal(respContent, &pageData)
	if err != nil {
		return nil, err
	}

	pageDataList, _ := pageData["RelatedTopics"].([]interface{})
	for _, row := range pageDataList {
		rowMap, _ := row.(map[string]interface{})
		topic, _ := rowMap["Name"].(string)
		if topic == "" {
			icon, _ := rowMap["Icon"].(map[string]interface{})
			results = append(results, map[string]string{
				"icon":  fmt.Sprintf("https://duckduckgo.com%s", icon["URL"].(string)),
				"text":  rowMap["Text"].(string),
				"topic": "",
				"url":   rowMap["FirstURL"].(string),
			})
		} else {
			subRowList, _ := rowMap["Topics"].([]interface{})
			for _, subRow := range subRowList {
				subRowMap, _ := subRow.(map[string]interface{})
				icon, _ := subRowMap["Icon"].(map[string]interface{})
				results = append(results, map[string]string{
					"icon":  fmt.Sprintf("https://duckduckgo.com%s", icon["URL"].(string)),
					"text":  subRowMap["Text"].(string),
					"topic": topic,
					"url":   subRowMap["FirstURL"].(string),
				})
			}
		}
	}

	return results, nil
}

/**
*	DuckDuckGo suggestions. Query params: https://duckduckgo.com/params.
*   region: wt-wt, us-en, uk-en, ru-ru, etc. Defaults to "wt-wt".
*
 */
func (a *AsyncDDGS) Suggestions(keywords string, region string) ([]map[string]string, error) {
	if keywords == "" {
		return nil, fmt.Errorf("keywords is mandatory")
	}
	if region == "" {
		region = "wt-wt"
	}

	payload := map[string]string{
		"q":  keywords,
		"kl": region,
	}

	respContent, err := a.agetURL("GET", "https://duckduckgo.com/ac", nil, payload)
	if err != nil {
		return nil, err
	}

	var pageData []map[string]string
	err = json.Unmarshal(respContent, &pageData)
	if err != nil {
		return nil, err
	}

	return pageData, nil
}

/**
*		from_: translate from (defaults automatically). Defaults to None.
*       to: what language to translate. Defaults to "en".
*   	   - zh-Hans
*   	   - zh-Hant
*   	   - de
*   	   - fr
*   	   - ja
*   	   - ko
**/
func (a *AsyncDDGS) Translate(keywords []string, from string, to string) ([]map[string]string, error) {
	if len(keywords) == 0 {
		return nil, fmt.Errorf("Keywords is mandatory")
	}
	if to == "" {
		to = "en"
	}

	vqd, err := a.agetVqd("translate")
	if err != nil {
		return nil, err
	}
	payload := map[string]string{
		"vqd":   vqd,
		"query": "translate",
		"to":    to,
	}

	if from != "" {
		payload["from"] = from
	}

	results := make([]map[string]string, 0)

	var wg sync.WaitGroup
	var mu sync.Mutex
	translateKeyword := func(s string) {
		defer wg.Done()
		respContent, err := a.agetURL("POST", "https://duckduckgo.com/translation.js", []byte(s), payload)
		if err != nil {
			return
		}
		var pageData map[string]interface{}
		if err = json.Unmarshal(respContent, &pageData); err != nil {
			return
		}

		if t, ok := pageData["translated"].(string); ok && len(t) > 0 {
			mu.Lock()
			results = append(results, map[string]string{s: t})
			mu.Unlock()
		}
	}
	for _, keyword := range keywords {
		wg.Add(1)
		go translateKeyword(keyword)

	}
	wg.Wait()

	return results, nil
}
