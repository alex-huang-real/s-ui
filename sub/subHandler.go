package sub

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/alireza0/s-ui/database"
	"github.com/alireza0/s-ui/database/model"
	"github.com/alireza0/s-ui/logger"
	"github.com/alireza0/s-ui/service"

	"github.com/gin-gonic/gin"
)

type SubHandler struct {
	service.SettingService
	SubService
	JsonService
	ClashService
}

func NewSubHandler(g *gin.RouterGroup) {
	a := &SubHandler{}
	a.initRouter(g)
}

func (s *SubHandler) initRouter(g *gin.RouterGroup) {
	g.GET("/:subid", s.serveSubscription)
	g.HEAD("/:subid", s.serveSubscription)
}

func (s *SubHandler) serveSubscription(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	client := authenticatedClient(c)
	if client == nil {
		c.Status(http.StatusNotFound)
		return
	}
	if c.Request.Method == http.MethodHead {
		s.addHeaders(c, s.SubService.getClientHeaders(client))
		c.Status(http.StatusOK)
		return
	}
	s.subs(c, client)
}

func authenticatedClient(c *gin.Context) *model.Client {
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil || len(query["token"]) != 1 || query["token"][0] == "" {
		return nil
	}
	token := query["token"][0]
	// Decode only the last segment, after routing. Unlike query decoding,
	// path decoding leaves '+' alone and lets an encoded slash be in a name.
	escapedPath := c.Request.URL.EscapedPath()
	name, err := url.PathUnescape(escapedPath[strings.LastIndexByte(escapedPath, '/')+1:])
	if err != nil {
		return nil
	}
	var clients []model.Client
	if err := database.GetDB().Where("enable = true AND name = ?", name).Find(&clients).Error; err != nil {
		logger.Error("subscription client lookup failed: ", err)
		return nil
	}
	// Older databases can contain duplicate names. Render the actual matching
	// row rather than resolving the name again after it has been authenticated.
	for i := range clients {
		if subtle.ConstantTimeCompare([]byte(clients[i].SubToken), []byte(token)) == 1 {
			return &clients[i]
		}
	}
	return nil
}

func (s *SubHandler) subs(c *gin.Context, client *model.Client) {
	var headers []string
	var result *string
	var err error
	format, isFormat := c.GetQuery("format")
	if !isFormat {
		c.Writer.Header().Add("Vary", "User-Agent")
		format = formatForUserAgent(c.Request.UserAgent())
	}
	switch {
	case format == "json":
		result, headers, err = s.JsonService.getJSON(client)
	case format == "clash":
		result, headers, err = s.ClashService.getClash(client)
	case format == "happ":
		result, headers, err = s.SubService.getSubs(client, happLinks)
	case !isFormat:
		result, headers, err = s.SubService.getSubs(client, nil)
	}
	if err != nil || result == nil {
		logger.Error(err)
		c.String(400, "Error!")
		return
	}

	s.addHeaders(c, headers)

	c.String(200, *result)
}

func (s *SubHandler) addHeaders(c *gin.Context, headers []string) {
	c.Writer.Header().Set("Subscription-Userinfo", headers[0])
	c.Writer.Header().Set("Profile-Update-Interval", headers[1])
	c.Writer.Header().Set("Profile-Title", headers[2])
	c.Writer.Header().Set("Content-Disposition", contentDispositionHeader(headers[2]))
}

func contentDispositionHeader(name string) string {
	filename := strings.TrimSpace(name)
	if filename == "" {
		filename = "subscription"
	}

	return fmt.Sprintf("attachment; filename=\"%s\"; filename*=UTF-8''%s", asciiSafeFilename(filename), rfc5987Encode(filename))
}

func asciiSafeFilename(filename string) string {
	var builder strings.Builder
	for _, r := range filename {
		switch {
		case r == '"' || r == '\\':
			builder.WriteByte('_')
		case r >= 0x20 && r <= 0x7e:
			builder.WriteRune(r)
		}
	}

	fallback := strings.TrimSpace(builder.String())
	if fallback == "" {
		return "subscription"
	}

	return fallback
}

func rfc5987Encode(filename string) string {
	const hex = "0123456789ABCDEF"

	var builder strings.Builder
	for _, b := range []byte(filename) {
		if isRFC5987AttrChar(b) {
			builder.WriteByte(b)
			continue
		}

		builder.WriteByte('%')
		builder.WriteByte(hex[b>>4])
		builder.WriteByte(hex[b&0x0f])
	}

	return builder.String()
}

func isRFC5987AttrChar(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z':
		return true
	case b >= 'A' && b <= 'Z':
		return true
	case b >= '0' && b <= '9':
		return true
	}

	switch b {
	case '!', '#', '$', '&', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	default:
		return false
	}
}
