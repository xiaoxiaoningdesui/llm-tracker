package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdlog "log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	ptypes "github.com/traefik/paerser/types"
	"github.com/traefik/traefik/v2/pkg/config/dynamic"
	"github.com/traefik/traefik/v2/pkg/log"
	"golang.org/x/net/http/httpguts"
)

// StatusClientClosedRequest non-standard HTTP status code for client disconnection.
const StatusClientClosedRequest = 499

// StatusClientClosedRequestText non-standard HTTP status for client disconnection.
const StatusClientClosedRequestText = "Client Closed Request"

// errorLogger is a logger instance used to log proxy errors.
// This logger is a shared instance as having one instance by proxy introduces a memory and go routine leak.
// The writer go routine is never stopped as the finalizer is never called.
// See https://github.com/sirupsen/logrus/blob/d1e6332644483cfee14de11099f03645561d55f8/writer.go#L57).
var errorLogger = stdlog.New(log.WithoutContext().WriterLevel(logrus.DebugLevel), "", 0)

func traceLlmRequest(outReq *http.Request) {
	if outReq.URL.Path == "/v1/chat/completions" || outReq.URL.Path == "/chat/completions" || outReq.URL.Path == "/api/chat" {
		var requestBody interface{}
		if outReq.Body != nil {
			bodyBytes, err := io.ReadAll(outReq.Body)
			if err != nil {
				fmt.Printf("llm request read request's body error : %v\n", err)
				return
			}
			outReq.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
			// 解析 JSON Body（如果是 JSON 请求）
			if strings.Contains(outReq.Header.Get("Content-Type"), "application/json") {
				if err := json.Unmarshal(bodyBytes, &requestBody); err != nil {
					fmt.Printf("traceLlmRequest: 请求体字节数组json反序列化失败: %v\n", err)
					return
				}
				if requestBodyMap, ok := requestBody.(map[string]interface{}); ok {
					if _, exists := requestBodyMap["tools"]; exists {
						// 将 tools 的值修改为字符串 "已屏蔽"
						requestBody.(map[string]interface{})["tools"] = "屏蔽"
					}
				}

			} else {
				// 如果不是 JSON，直接存为字符串
				requestBody = string(bodyBytes)
			}
			now := time.Now()

			// 格式化到秒
			// 3. 构造 JSON 数据
			logData := map[string]interface{}{
				"类型":  "LLM输入",
				"时间":  now.Format("2006-01-02 15:04:05"),
				"请求体": requestBody,
			}

			jsonData, err := json.MarshalIndent(logData, "", "  ")
			if err != nil {
				fmt.Printf("llm final request json dump error: %v\n", err)
			} else {
				fmt.Printf(unescapeRequest(string(jsonData)))
				log.WithoutContext().Infof(string(jsonData))
				fmt.Print("\n\n")
				fmt.Println("\033[38;5;201m" + strings.Repeat(">>//=|=|=|=>  \033[38;5;51m【\033[1m\033[38;5;208m以上内容为用户输入\033[0m\033[38;5;51m】\033[38;5;201m <=|=|=|=//<<\033[0m\n", 1) + "\033[0m")
			}
		}
	}
}

func buildProxy(passHostHeader *bool, responseForwarding *dynamic.ResponseForwarding, roundTripper http.RoundTripper, bufferPool httputil.BufferPool) (http.Handler, error) {
	var flushInterval ptypes.Duration
	if responseForwarding != nil {
		err := flushInterval.Set(responseForwarding.FlushInterval)
		if err != nil {
			return nil, fmt.Errorf("error creating flush interval: %w", err)
		}
	}
	if flushInterval == 0 {
		flushInterval = ptypes.Duration(100 * time.Millisecond)
	}

	proxy := &ReverseProxy{
		Director: func(outReq *http.Request) {
			u := outReq.URL
			if outReq.RequestURI != "" {
				parsedURL, err := url.ParseRequestURI(outReq.RequestURI)
				if err == nil {
					u = parsedURL
				}
			}
			outReq.URL.Path = u.Path
			outReq.URL.RawPath = u.RawPath
			traceLlmRequest(outReq)
			// If a plugin/middleware adds semicolons in query params, they should be urlEncoded.
			outReq.URL.RawQuery = strings.ReplaceAll(u.RawQuery, ";", "&")
			outReq.RequestURI = "" // Outgoing request should not have RequestURI
			outReq.Proto = "HTTP/1.1"
			outReq.ProtoMajor = 1
			outReq.ProtoMinor = 1
			//不加deepseek会报错
			outReq.Host = outReq.URL.Hostname()
			// Do not pass client Host header unless optsetter PassHostHeader is set.
			if passHostHeader != nil && !*passHostHeader {
				outReq.Host = outReq.URL.Host
			}

			// Even if the websocket RFC says that headers should be case-insensitive,
			// some servers need Sec-WebSocket-Key, Sec-WebSocket-Extensions, Sec-WebSocket-Accept,
			// Sec-WebSocket-Protocol and Sec-WebSocket-Version to be case-sensitive.
			// https://tools.ietf.org/html/rfc6455#page-20
			if isWebSocketUpgrade(outReq) {
				outReq.Header["Sec-WebSocket-Key"] = outReq.Header["Sec-Websocket-Key"]
				outReq.Header["Sec-WebSocket-Extensions"] = outReq.Header["Sec-Websocket-Extensions"]
				outReq.Header["Sec-WebSocket-Accept"] = outReq.Header["Sec-Websocket-Accept"]
				outReq.Header["Sec-WebSocket-Protocol"] = outReq.Header["Sec-Websocket-Protocol"]
				outReq.Header["Sec-WebSocket-Version"] = outReq.Header["Sec-Websocket-Version"]
				delete(outReq.Header, "Sec-Websocket-Key")
				delete(outReq.Header, "Sec-Websocket-Extensions")
				delete(outReq.Header, "Sec-Websocket-Accept")
				delete(outReq.Header, "Sec-Websocket-Protocol")
				delete(outReq.Header, "Sec-Websocket-Version")
			}
		},
		Transport:     roundTripper,
		FlushInterval: time.Duration(flushInterval),
		BufferPool:    bufferPool,
		ErrorLog:      errorLogger,
		ErrorHandler: func(w http.ResponseWriter, request *http.Request, err error) {
			statusCode := http.StatusInternalServerError

			switch {
			case errors.Is(err, io.EOF):
				statusCode = http.StatusBadGateway
			case errors.Is(err, context.Canceled):
				statusCode = StatusClientClosedRequest
			default:
				var netErr net.Error
				if errors.As(err, &netErr) {
					if netErr.Timeout() {
						statusCode = http.StatusGatewayTimeout
					} else {
						statusCode = http.StatusBadGateway
					}
				}
			}

			log.Debugf("'%d %s' caused by: %v", statusCode, statusText(statusCode), err)
			w.WriteHeader(statusCode)
			_, werr := w.Write([]byte(statusText(statusCode)))
			if werr != nil {
				log.Debugf("Error while writing status code", werr)
			}
		},
	}

	return proxy, nil
}
func unescapeRequest(raw string) string {
	formatted := strings.ReplaceAll(raw, "\\n", "\n")
	formatted = strings.ReplaceAll(formatted, "\\\"", "\"")
	formatted = strings.ReplaceAll(formatted, "\\u0026", "&")
	formatted = strings.ReplaceAll(formatted, "\\u003e", ">")
	formatted = strings.ReplaceAll(formatted, "\\u002f", "/")

	// 2. URL解码
	decoded, err := url.QueryUnescape(formatted)
	if err == nil {
		formatted = decoded // 如果解码失败，使用原始文本
	}

	// 3. 替换HTML标签
	re := regexp.MustCompile(`<[^>]+>`)
	formatted = re.ReplaceAllString(formatted, "")

	// 4. 按行分割并添加缩进
	lines := strings.Split(formatted, "\n")
	for i, line := range lines {
		if strings.Contains(line, "- ") {
			// 根据层级添加缩进
			level := strings.Count(line, "- ") - 1
			lines[i] = strings.Repeat("  ", level) + line
		}
	}
	formatted = strings.Join(lines, "\n")

	return formatted
}
func isWebSocketUpgrade(req *http.Request) bool {
	if !httpguts.HeaderValuesContainsToken(req.Header["Connection"], "Upgrade") {
		return false
	}

	return strings.EqualFold(req.Header.Get("Upgrade"), "websocket")
}

func statusText(statusCode int) string {
	if statusCode == StatusClientClosedRequest {
		return StatusClientClosedRequestText
	}
	return http.StatusText(statusCode)
}
