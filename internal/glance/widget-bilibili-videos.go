package glance

import (
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	bilibiliVideosWidgetTemplate             = mustParseTemplate("videos.html", "widget-base.html", "video-card-contents.html")
	bilibiliVideosWidgetGridTemplate         = mustParseTemplate("videos-grid.html", "widget-base.html", "video-card-contents.html")
	bilibiliVideosWidgetVerticalListTemplate = mustParseTemplate("videos-vertical-list.html", "widget-base.html")
)

type bilibiliVideosWidget struct {
	widgetBase        `yaml:",inline"`
	Videos            bilibiliVideoList `yaml:"-"`
	VideoUrlTemplate  string            `yaml:"video-url-template"`
	Style             string            `yaml:"style"`
	CollapseAfter     int               `yaml:"collapse-after"`
	CollapseAfterRows int               `yaml:"collapse-after-rows"`
	RSSHubUrls        []string          `yaml:"rsshuburls"`
	Limit             int               `yaml:"limit"`
	IncludeShorts     bool              `yaml:"include-shorts"`
	ImageProxy        string            `yaml:"image-proxy"`
}

func (widget *bilibiliVideosWidget) initialize() error {
	widget.withTitle("Videos").withCacheDuration(time.Hour)

	if widget.Limit <= 0 {
		widget.Limit = 25
	}

	if widget.CollapseAfterRows == 0 || widget.CollapseAfterRows < -1 {
		widget.CollapseAfterRows = 4
	}

	if widget.CollapseAfter == 0 || widget.CollapseAfter < -1 {
		widget.CollapseAfter = 7
	}

	if widget.ImageProxy == "" {
		widget.ImageProxy = "//wsrv.nl/?url="
	}

	return nil
}

func (widget *bilibiliVideosWidget) update(ctx context.Context) {
	videos, err := fetchBilibiliChannelUploads(widget.RSSHubUrls, widget.VideoUrlTemplate, widget.IncludeShorts, widget.ImageProxy)

	if !widget.canContinueUpdateAfterHandlingErr(err) {
		return
	}

	if len(videos) > widget.Limit {
		videos = videos[:widget.Limit]
	}

	widget.Videos = videos
}

func (widget *bilibiliVideosWidget) Render() template.HTML {
	var template *template.Template

	switch widget.Style {
	case "grid-cards":
		template = bilibiliVideosWidgetGridTemplate
	case "vertical-list":
		template = bilibiliVideosWidgetVerticalListTemplate
	default:
		template = bilibiliVideosWidgetTemplate
	}

	return widget.renderTemplate(widget, template)
}

type bilibiliFeedResponseJson struct {
	Version     string `json:"version"`
	Title       string `json:"title"`
	HomePageURL string `json:"home_page_url"`
	Description string `json:"description"`
	Language    string `json:"language"`
	Items       []struct {
		ID            string    `json:"id"`
		URL           string    `json:"url"`
		Title         string    `json:"title"`
		ContentHTML   string    `json:"content_html"`
		DatePublished time.Time `json:"date_published"` // 使用 time.Time 类型来解析日期
		Authors       []struct {
			Name string `json:"name"`
		}
	}
}

type bilibiliVideo struct {
	ThumbnailUrl string
	Title        string
	Url          string
	Author       string
	AuthorUrl    string
	TimePosted   time.Time
}

type bilibiliVideoList []bilibiliVideo

func (v bilibiliVideoList) sortByNewest() bilibiliVideoList {
	sort.Slice(v, func(i, j int) bool {
		return v[i].TimePosted.After(v[j].TimePosted)
	})

	return v
}

func fetchBilibiliChannelUploads(RSSHubUrls []string, videoUrlTemplate string, includeShorts bool, imageProxy string) (bilibiliVideoList, error) {
	requests := make([]*http.Request, 0, len(RSSHubUrls))

	for _, feedUrl := range RSSHubUrls {
		request, _ := http.NewRequest("GET", feedUrl, nil)
		requests = append(requests, request)
	}

	job := newJob(decodeJsonFromRequestTask[bilibiliFeedResponseJson](defaultHTTPClient), requests).withWorkers(30)
	responses, errs, err := workerPoolDo(job)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNoContent, err)
	}

	videos := make(bilibiliVideoList, 0, len(RSSHubUrls)*15)
	var failed int

	for i := range responses {
		if errs[i] != nil {
			failed++
			slog.Error("Failed to fetch youtube feed", "rsshub url", RSSHubUrls[i], "error", errs[i])
			continue
		}

		response := responses[i]

		for j := range response.Items {
			v := &response.Items[j]
			// 编译正则表达式
			re := regexp.MustCompile(`<img[^>]+src="([^"]+)"`)
			// 查找所有匹配项
			matches := re.FindAllStringSubmatch(v.ContentHTML, -1)
			// 提取图像 URL
			imageURLs := make([]string, 0, len(matches))
			for _, match := range matches {
				if len(match) > 1 {
					imageURLs = append(imageURLs, match[1])
				}
			}
			authorNames := make([]string, len(v.Authors))
			for i, author := range v.Authors {
				authorNames[i] = author.Name
			}

			videos = append(videos, bilibiliVideo{
				ThumbnailUrl: imageProxy + imageURLs[0],
				Title:        v.Title,
				Url:          v.URL,
				Author:       strings.Join(authorNames, ", "),
				AuthorUrl:    v.URL,
				TimePosted:   v.DatePublished,
			})
		}
	}

	if len(videos) == 0 {
		return nil, errNoContent
	}

	videos.sortByNewest()

	if failed > 0 {
		return videos, fmt.Errorf("%w: missing videos from %d channels", errPartialContent, failed)
	}

	return videos, nil
}
