package service

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	logrus "github.com/traefik/traefik/v2/pkg/log"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/textproto"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/net/http/httpguts"
)

// A ProxyRequest contains a request to be rewritten by a [ReverseProxy].
type ProxyRequest struct {
	// In is the request received by the proxy.
	// The Rewrite function must not modify In.
	In *http.Request

	// Out is the request which will be sent by the proxy.
	// The Rewrite function may modify or replace this request.
	// Hop-by-hop headers are removed from this request
	// before Rewrite is called.
	Out *http.Request
}

// SetURL routes the outbound request to the scheme, host, and base path
// provided in target. If the target's path is "/base" and the incoming
// request was for "/dir", the target request will be for "/base/dir".
//
// SetURL rewrites the outbound Host header to match the target's host.
// To preserve the inbound request's Host header (the default behavior
// of [NewSingleHostReverseProxy]):
//
//	rewriteFunc := func(r *httputil.ProxyRequest) {
//		r.SetURL(url)
//		r.Out.Host = r.In.Host
//	}
func (r *ProxyRequest) SetURL(target *url.URL) {
	rewriteRequestURL(r.Out, target)
	r.Out.Host = ""
}

// SetXForwarded sets the X-Forwarded-For, X-Forwarded-Host, and
// X-Forwarded-Proto headers of the outbound request.
//
//   - The X-Forwarded-For header is set to the client IP address.
//   - The X-Forwarded-Host header is set to the host name requested
//     by the client.
//   - The X-Forwarded-Proto header is set to "http" or "https", depending
//     on whether the inbound request was made on a TLS-enabled connection.
//
// If the outbound request contains an existing X-Forwarded-For header,
// SetXForwarded appends the client IP address to it. To append to the
// inbound request's X-Forwarded-For header (the default behavior of
// [ReverseProxy] when using a Director function), copy the header
// from the inbound request before calling SetXForwarded:
//
//	rewriteFunc := func(r *httputil.ProxyRequest) {
//		r.Out.Header["X-Forwarded-For"] = r.In.Header["X-Forwarded-For"]
//		r.SetXForwarded()
//	}
func (r *ProxyRequest) SetXForwarded() {
	clientIP, _, err := net.SplitHostPort(r.In.RemoteAddr)
	if err == nil {
		prior := r.Out.Header["X-Forwarded-For"]
		if len(prior) > 0 {
			clientIP = strings.Join(prior, ", ") + ", " + clientIP
		}
		r.Out.Header.Set("X-Forwarded-For", clientIP)
	} else {
		r.Out.Header.Del("X-Forwarded-For")
	}
	r.Out.Header.Set("X-Forwarded-Host", r.In.Host)
	if r.In.TLS == nil {
		r.Out.Header.Set("X-Forwarded-Proto", "http")
	} else {
		r.Out.Header.Set("X-Forwarded-Proto", "https")
	}
}

// ReverseProxy is an HTTP Handler that takes an incoming request and
// sends it to another server, proxying the response back to the
// client.
//
// 1xx responses are forwarded to the client if the underlying
// transport supports ClientTrace.Got1xxResponse.
type ReverseProxy struct {
	// Rewrite must be a function which modifies
	// the request into a new request to be sent
	// using Transport. Its response is then copied
	// back to the original client unmodified.
	// Rewrite must not access the provided ProxyRequest
	// or its contents after returning.
	//
	// The Forwarded, X-Forwarded, X-Forwarded-Host,
	// and X-Forwarded-Proto headers are removed from the
	// outbound request before Rewrite is called. See also
	// the ProxyRequest.SetXForwarded method.
	//
	// Unparsable query parameters are removed from the
	// outbound request before Rewrite is called.
	// The Rewrite function may copy the inbound URL's
	// RawQuery to the outbound URL to preserve the original
	// parameter string. Note that this can lead to security
	// issues if the proxy's interpretation of query parameters
	// does not match that of the downstream server.
	//
	// At most one of Rewrite or Director may be set.
	Rewrite func(*ProxyRequest)

	// Director is a function which modifies
	// the request into a new request to be sent
	// using Transport. Its response is then copied
	// back to the original client unmodified.
	// Director must not access the provided Request
	// after returning.
	//
	// By default, the X-Forwarded-For header is set to the
	// value of the client IP address. If an X-Forwarded-For
	// header already exists, the client IP is appended to the
	// existing values. As a special case, if the header
	// exists in the Request.Header map but has a nil value
	// (such as when set by the Director func), the X-Forwarded-For
	// header is not modified.
	//
	// To prevent IP spoofing, be sure to delete any pre-existing
	// X-Forwarded-For header coming from the client or
	// an untrusted proxy.
	//
	// Hop-by-hop headers are removed from the request after
	// Director returns, which can remove headers added by
	// Director. Use a Rewrite function instead to ensure
	// modifications to the request are preserved.
	//
	// Unparsable query parameters are removed from the outbound
	// request if Request.Form is set after Director returns.
	//
	// At most one of Rewrite or Director may be set.
	Director func(*http.Request)

	// The transport used to perform proxy requests.
	// If nil, http.DefaultTransport is used.
	Transport http.RoundTripper

	// FlushInterval specifies the flush interval
	// to flush to the client while copying the
	// response body.
	// If zero, no periodic flushing is done.
	// A negative value means to flush immediately
	// after each write to the client.
	// The FlushInterval is ignored when ReverseProxy
	// recognizes a response as a streaming response, or
	// if its ContentLength is -1; for such responses, writes
	// are flushed to the client immediately.
	FlushInterval time.Duration

	// ErrorLog specifies an optional logger for errors
	// that occur when attempting to proxy the request.
	// If nil, logging is done via the log package's standard logger.
	ErrorLog *log.Logger

	// BufferPool optionally specifies a buffer pool to
	// get byte slices for use by io.CopyBuffer when
	// copying HTTP response bodies.
	BufferPool BufferPool

	// ModifyResponse is an optional function that modifies the
	// Response from the backend. It is called if the backend
	// returns a response at all, with any HTTP status code.
	// If the backend is unreachable, the optional ErrorHandler is
	// called without any call to ModifyResponse.
	//
	// If ModifyResponse returns an error, ErrorHandler is called
	// with its error value. If ErrorHandler is nil, its default
	// implementation is used.
	ModifyResponse func(*http.Response) error

	// ErrorHandler is an optional function that handles errors
	// reaching the backend or errors from ModifyResponse.
	//
	// If nil, the default is to log the provided error and return
	// a 502 Status Bad Gateway response.
	ErrorHandler func(http.ResponseWriter, *http.Request, error)
}

// A BufferPool is an interface for getting and returning temporary
// byte slices for use by [io.CopyBuffer].
type BufferPool interface {
	Get() []byte
	Put([]byte)
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}

func joinURLPath(a, b *url.URL) (path, rawpath string) {
	if a.RawPath == "" && b.RawPath == "" {
		return singleJoiningSlash(a.Path, b.Path), ""
	}
	// Same as singleJoiningSlash, but uses EscapedPath to determine
	// whether a slash should be added
	apath := a.EscapedPath()
	bpath := b.EscapedPath()

	aslash := strings.HasSuffix(apath, "/")
	bslash := strings.HasPrefix(bpath, "/")

	switch {
	case aslash && bslash:
		return a.Path + b.Path[1:], apath + bpath[1:]
	case !aslash && !bslash:
		return a.Path + "/" + b.Path, apath + "/" + bpath
	}
	return a.Path + b.Path, apath + bpath
}

// NewSingleHostReverseProxy returns a new [ReverseProxy] that routes
// URLs to the scheme, host, and base path provided in target. If the
// target's path is "/base" and the incoming request was for "/dir",
// the target request will be for /base/dir.
//
// NewSingleHostReverseProxy does not rewrite the Host header.
//
// To customize the ReverseProxy behavior beyond what
// NewSingleHostReverseProxy provides, use ReverseProxy directly
// with a Rewrite function. The ProxyRequest SetURL method
// may be used to route the outbound request. (Note that SetURL,
// unlike NewSingleHostReverseProxy, rewrites the Host header
// of the outbound request by default.)
//
//	proxy := &ReverseProxy{
//		Rewrite: func(r *ProxyRequest) {
//			r.SetURL(target)
//			r.Out.Host = r.In.Host // if desired
//		},
//	}
func NewSingleHostReverseProxy(target *url.URL) *ReverseProxy {
	director := func(req *http.Request) {
		rewriteRequestURL(req, target)
	}
	return &ReverseProxy{Director: director}
}

func rewriteRequestURL(req *http.Request, target *url.URL) {
	targetQuery := target.RawQuery
	req.URL.Scheme = target.Scheme
	req.URL.Host = target.Host
	req.URL.Path, req.URL.RawPath = joinURLPath(target, req.URL)
	if targetQuery == "" || req.URL.RawQuery == "" {
		req.URL.RawQuery = targetQuery + req.URL.RawQuery
	} else {
		req.URL.RawQuery = targetQuery + "&" + req.URL.RawQuery
	}
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

// Hop-by-hop headers. These are removed when sent to the backend.
// As of RFC 7230, hop-by-hop headers are required to appear in the
// Connection header field. These are the headers defined by the
// obsoleted RFC 2616 (section 13.5.1) and are used for backward
// compatibility.
var hopHeaders = []string{
	"Connection",
	"Proxy-Connection", // non-standard but still sent by libcurl and rejected by e.g. google
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",      // canonicalized version of "TE"
	"Trailer", // not Trailers per URL above; https://www.rfc-editor.org/errata_search.php?eid=4522
	"Transfer-Encoding",
	"Upgrade",
}

func (p *ReverseProxy) defaultErrorHandler(rw http.ResponseWriter, req *http.Request, err error) {
	p.logf("http: proxy error: %v", err)
	rw.WriteHeader(http.StatusBadGateway)
}

func (p *ReverseProxy) getErrorHandler() func(http.ResponseWriter, *http.Request, error) {
	if p.ErrorHandler != nil {
		return p.ErrorHandler
	}
	return p.defaultErrorHandler
}

func (p *ReverseProxy) modifyResponsePre(rw http.ResponseWriter, res *http.Response, req *http.Request) *io.PipeWriter {

	contentType := res.Header.Get("Content-Type")

	llmType := "ollama"
	if contentType != "" && (contentType == "application/x-ndjson" || contentType == "text/event-stream; charset=utf-8") {
	} else {
		return nil
	}
	if contentType == "text/event-stream; charset=utf-8" {
		llmType = "deepseek"
	}

	pr, pw := io.Pipe()
	tee := io.TeeReader(res.Body, pw)
	res.Body = io.NopCloser(tee)
	closeNotifier := rw.(http.CloseNotifier)
	clientClosed := make(chan bool, 1)
	go func() {
		select {
		case <-closeNotifier.CloseNotify():
			clientClosed <- true
		}
	}()

	go func() {
		scanner := bufio.NewScanner(pr)
		var finalContent strings.Builder
		var lastLine string
		var toolCallsCache []map[string]interface{}
		for scanner.Scan() {
			if llmType == "ollama" {
				line := scanner.Bytes()
				lastLine = string(line)
				var chunkData struct {
					Message struct {
						Content   string                   `json:"content"`
						ToolCalls []map[string]interface{} `json:"toolCalls"`
					} `json:"message"`
				}

				if err := json.Unmarshal(line, &chunkData); err != nil {
					fmt.Printf("Error, ollama type, 每行chunk文本json解码失败: %v\n", err)
					return
				}
				var content = chunkData.Message.Content
				if content != "" {
					finalContent.WriteString(chunkData.Message.Content)
				}
				if len(chunkData.Message.ToolCalls) == 1 {
					toolCallsCache = append(toolCallsCache, chunkData.Message.ToolCalls[0])
				}
			} else {
				line := scanner.Bytes()
				if strings.TrimSpace(string(line)) == "" {
					continue
				}
				if string(line) == "data: [DONE]" {
					break
				}
				lastLine = string(line)
				jsonData := strings.TrimPrefix(lastLine, "data: ")
				var parsedData map[string]interface{}
				err := json.Unmarshal([]byte(jsonData), &parsedData)
				if err != nil {
					fmt.Printf("Error, deepseek type，每行chunk文本json解码失败: %v\n", err)
					return
				}

				choices, ok := parsedData["choices"].([]interface{})
				if !ok {
					fmt.Println("Error, deepseek type, choices's value can't convert to []interface{}")
					return
				}

				// 检查 choices 数组是否为空
				if len(choices) == 0 {
					fmt.Println("Error, deepseek type, choices's value length is zero")
					return
				}

				// 获取第一个 choice
				firstChoice, ok := choices[0].(map[string]interface{})
				if !ok {
					fmt.Println("Error, deepseek type, first choice can't convert to map[string]interface{}")
					return
				}

				// 获取 delta
				delta, ok := firstChoice["delta"].(map[string]interface{})
				if !ok {
					fmt.Println("Error, deepseek type, delta's value can't conver to map[string]interface{}")
					return
				}

				// 获取 content
				if content, ok := delta["content"].(string); ok {
					// 存在 content 字段且是字符串类型
					finalContent.WriteString(content)
					continue // 处理完 content 后继续下一行
				}
				if toolCalls, ok := delta["tool_calls"].([]interface{}); ok && len(toolCalls) > 0 {
					for _, toolCall := range toolCalls {
						toolCallMap, ok := toolCall.(map[string]interface{})
						if !ok {
							continue
						}

						// 检查是否有 index（DeepSeek 流式返回的 tool_calls 通常带 index）
						index, hasIndex := toolCallMap["index"].(float64) // JSON 数字默认解析为 float64
						if !hasIndex {
							continue
						}
						idx := int(index) // 转为 int 便于索引

						// 确保 toolCallsCache 足够大（防止越界）
						if idx >= len(toolCallsCache) {
							newCache := make([]map[string]interface{}, idx+1)
							copy(newCache, toolCallsCache)
							toolCallsCache = newCache
						}

						// 检查是否有 id（第一次通常会带 id）
						if _, hasID := toolCallMap["id"].(string); hasID {
							// 如果是新的 tool_call，缓存整个结构
							toolCallsCache[idx] = toolCallMap
						} else if toolCallsCache[idx] != nil {
							// 否则，如果该 index 已有缓存，则更新 arguments
							cachedFunction, ok := toolCallsCache[idx]["function"].(map[string]interface{})
							if !ok {
								continue
							}

							// 获取新的 arguments
							if function, ok := toolCallMap["function"].(map[string]interface{}); ok {
								if newArgs, ok := function["arguments"].(string); ok && newArgs != "" {
									// 追加或设置 arguments
									if existingArgs, ok := cachedFunction["arguments"].(string); ok {
										cachedFunction["arguments"] = existingArgs + newArgs
									} else {
										cachedFunction["arguments"] = newArgs
									}
								}
							}
						}
					}
					continue
				}
			}

		}

		if err := scanner.Err(); err != nil {
			fmt.Printf("scanner读取失败： %v", err)
			return
		}

		select {
		case <-clientClosed:
			fmt.Println("客户端提前关闭连接")
		default:
			if llmType == "ollama" {
				var data map[string]interface{}
				err := json.Unmarshal([]byte(lastLine), &data)
				if err != nil {
					fmt.Printf("Error, ollama type, last line json loads error: %v\n", err)
					return
				}
				message, ok := data["message"].(map[string]interface{})
				if !ok {
					fmt.Println("Error, ollama type, message's value can't convert to map[string]interface{}")
					return
				}
				message["content"] = finalContent.String()
				if len(toolCallsCache) >= 1 {
					message["tool_calls"] = toolCallsCache
				}

				logData := map[string]interface{}{
					"类型":  "LLM输出",
					"时间":  time.Now().Format("2006-01-02 15:04:05"),
					"响应体": data,
				}

				jsonLog, err := json.MarshalIndent(logData, "", "  ")
				if err != nil {
					fmt.Printf("ollama type, json dumps llm totaal resposne error: %v\n", err)
				} else {
					fmt.Printf(unescape(string(jsonLog)))
					logrus.WithoutContext().Infof(string(jsonLog))
					fmt.Print("\n\n")

					fmt.Println("\033[38;5;201m" + strings.Repeat(">>//=|=|=|=>  \033[38;5;51m【\033[1m\033[38;5;226m实时信道激活. 以上内容为大模型输出\033[0m\033[38;5;51m】\033[38;5;201m <=|=|=|=//<<\033[0m\n", 1) + "\033[0m")
				}
			} else {
				jsonData := strings.TrimPrefix(lastLine, "data: ")

				// 定义一个 map 来存储解析后的 JSON 数据
				var parsedData map[string]interface{}
				err := json.Unmarshal([]byte(jsonData), &parsedData)
				if err != nil {
					fmt.Printf("deepseek type, lastline json loads map error: %v\n", err)
					return
				}

				// 获取 choices 数组
				choices, ok := parsedData["choices"].([]interface{})
				if !ok {
					fmt.Println("Error: choices's value can't convert to []interface{}")
					return
				}

				// 检查 choices 数组是否为空
				if len(choices) == 0 {
					fmt.Println("Error: choices's value list length is zero")
					return
				}

				// 获取第一个 choice
				firstChoice, ok := choices[0].(map[string]interface{})
				if !ok {
					fmt.Println("Error: first choices can't convert to map[string]interface{}")
					return
				}

				// 获取 delta
				delta, ok := firstChoice["delta"].(map[string]interface{})
				if !ok {
					fmt.Println("Error: firstChoice['delta']'s value can't convert to map[string]interface{}")
					return
				}

				// 替换 content 的值
				delta["content"] = finalContent.String()
				if len(toolCallsCache) >= 1 {
					delta["tool_calls"] = toolCallsCache
				}

				logData := map[string]interface{}{
					"类型":  "LLM输出",
					"时间":  time.Now().Format("2006-01-02 15:04:05"),
					"响应体": parsedData,
				}

				jsonLog, err := json.MarshalIndent(logData, "", "  ")
				if err != nil {
					fmt.Printf("deepseek type, final result json dump error: %v\n", err)
				} else {
					fmt.Println(unescape(string(jsonLog)))
					logrus.WithoutContext().Infof(string(jsonLog))
					fmt.Print("\n\n")

					fmt.Println("\033[38;5;201m" + strings.Repeat(">>//=|=|=|=>  \033[38;5;51m【\033[1m\033[38;5;226m实时信道激活. 以上内容为大模型输出\033[0m\033[38;5;51m】\033[38;5;201m <=|=|=|=//<<\033[0m\n", 1) + "\033[0m")
				}
			}
		}
	}()
	return pw

}

// modifyResponse conditionally runs the optional ModifyResponse hook
// and reports whether the request should proceed.
func (p *ReverseProxy) modifyResponse(rw http.ResponseWriter, res *http.Response, req *http.Request) bool {
	if p.ModifyResponse == nil {
		return true
	}
	if err := p.ModifyResponse(res); err != nil {
		res.Body.Close()
		p.getErrorHandler()(rw, req, err)
		return false
	}
	return true
}

func traceLlmNotStreamResponse(res *http.Response) {
	rawBodyBytes, err := io.ReadAll(res.Body)
	if err != nil {
		fmt.Printf("traceLlmResponse: 读取响应体失败: %v\n", err)
		return
	}
	res.Body.Close()     // 关闭原始 Body
	var bodyBytes []byte // 用于日志的解压数据（如果是 gzip）
	isGzipped := res.Header.Get("Content-Encoding") == "gzip"
	if isGzipped {
		gzReader, err := gzip.NewReader(bytes.NewReader(rawBodyBytes))
		if err != nil {
			fmt.Printf("traceLlmResponse: gzip数据读取失败: %v\n", err)
			return
		}
		defer gzReader.Close()
		bodyBytes, err = io.ReadAll(gzReader)
		if err != nil {
			fmt.Printf("traceLlmResponse: gzipreader中读取数据失败: %v\n", err)
			return
		}
	} else {
		bodyBytes = rawBodyBytes // 非 gzip，直接使用原始数据
	}

	// 2. 恢复原始 Body（保持 gzip 压缩，客户端需要）
	res.Body = io.NopCloser(bytes.NewReader(rawBodyBytes))

	// 3. 后续日志处理（使用 bodyBytes）
	var prettyJSON bytes.Buffer
	if json.Indent(&prettyJSON, bodyBytes, "", "  ") != nil {
		// 如果不是 JSON，直接使用原始数据
		fmt.Println("NOT JSON, may be streaming response")
		prettyJSON = *bytes.NewBuffer(bodyBytes)
	}
	logData := map[string]interface{}{
		"类型":  "LLM输出",
		"时间":  time.Now().Format("2006-01-02 15:04:05"),
		"响应体": json.RawMessage(prettyJSON.Bytes()),
	}

	jsonLog, err := json.MarshalIndent(logData, "", "  ")
	if err != nil {
		fmt.Printf("traceLlmResponse: json编码为二进制数组失败: %v\n", err)
		//fmt.Println(string(prettyJSON.Bytes()))

	} else {
		fmt.Printf(unescape(string(jsonLog)))
		logrus.WithoutContext().Infof(string(jsonLog))
		fmt.Print("\n\n")

		fmt.Println("\033[38;5;201m" + strings.Repeat(">>//=|=|=|=>  \033[38;5;51m【\033[1m\033[38;5;226m静态数据激活. 以上内容为大模型输出\033[0m\033[38;5;51m】\033[38;5;201m <=|=|=|=//<<\033[0m\n", 1) + "\033[0m")

	}
}

func unescape(raw string) string {
	formatted := strings.ReplaceAll(raw, "\\n", "\n")
	formatted = strings.ReplaceAll(formatted, "\\\"", "\"")
	formatted = strings.ReplaceAll(formatted, "\\u0026", "&")
	formatted = strings.ReplaceAll(formatted, "\\u003c", "<")
	formatted = strings.ReplaceAll(formatted, "\\u003e", ">")

	// 2. URL解码
	decoded, err := url.QueryUnescape(formatted)
	if err != nil {
		decoded = formatted // 如果解码失败，使用原始文本
	}
	// 3. 按行分割并添加缩进
	lines := strings.Split(decoded, "\n")
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

func (p *ReverseProxy) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	transport := p.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}

	ctx := req.Context()
	if ctx.Done() != nil {
		// CloseNotifier predates context.Context, and has been
		// entirely superseded by it. If the request contains
		// a Context that carries a cancellation signal, don't
		// bother spinning up a goroutine to watch the CloseNotify
		// channel (if any).
		//
		// If the request Context has a nil Done channel (which
		// means it is either context.Background, or a custom
		// Context implementation with no cancellation signal),
		// then consult the CloseNotifier if available.
	} else if cn, ok := rw.(http.CloseNotifier); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		notifyChan := cn.CloseNotify()
		go func() {
			select {
			case <-notifyChan:
				cancel()
			case <-ctx.Done():
			}
		}()
	}

	outreq := req.Clone(ctx)
	if req.ContentLength == 0 {
		outreq.Body = nil // Issue 16036: nil Body for http.Transport retries
	}
	if outreq.Body != nil {
		// Reading from the request body after returning from a handler is not
		// allowed, and the RoundTrip goroutine that reads the Body can outlive
		// this handler. This can lead to a crash if the handler panics (see
		// Issue 46866). Although calling Close doesn't guarantee there isn't
		// any Read in flight after the handle returns, in practice it's safe to
		// read after closing it.
		defer outreq.Body.Close()
	}
	if outreq.Header == nil {
		outreq.Header = make(http.Header) // Issue 33142: historical behavior was to always allocate
	}

	if (p.Director != nil) == (p.Rewrite != nil) {
		p.getErrorHandler()(rw, req, errors.New("ReverseProxy must have exactly one of Director or Rewrite set"))
		return
	}

	if p.Director != nil {
		p.Director(outreq)
		if outreq.Form != nil {
			outreq.URL.RawQuery = cleanQueryParams(outreq.URL.RawQuery)
		}
	}
	outreq.Close = false

	reqUpType := upgradeType(outreq.Header)
	if !IsPrint(reqUpType) {
		p.getErrorHandler()(rw, req, fmt.Errorf("client tried to switch to invalid protocol %q", reqUpType))
		return
	}
	removeHopByHopHeaders(outreq.Header)

	// Issue 21096: tell backend applications that care about trailer support
	// that we support trailers. (We do, but we don't go out of our way to
	// advertise that unless the incoming client request thought it was worth
	// mentioning.) Note that we look at req.Header, not outreq.Header, since
	// the latter has passed through removeHopByHopHeaders.
	if httpguts.HeaderValuesContainsToken(req.Header["Te"], "trailers") {
		outreq.Header.Set("Te", "trailers")
	}

	// After stripping all the hop-by-hop connection headers above, add back any
	// necessary for protocol upgrades, such as for websockets.
	if reqUpType != "" {
		outreq.Header.Set("Connection", "Upgrade")
		outreq.Header.Set("Upgrade", reqUpType)
	}

	if p.Rewrite != nil {
		// Strip client-provided forwarding headers.
		// The Rewrite func may use SetXForwarded to set new values
		// for these or copy the previous values from the inbound request.
		outreq.Header.Del("Forwarded")
		outreq.Header.Del("X-Forwarded-For")
		outreq.Header.Del("X-Forwarded-Host")
		outreq.Header.Del("X-Forwarded-Proto")

		// Remove unparsable query parameters from the outbound request.
		outreq.URL.RawQuery = cleanQueryParams(outreq.URL.RawQuery)

		pr := &ProxyRequest{
			In:  req,
			Out: outreq,
		}
		p.Rewrite(pr)
		outreq = pr.Out
	} else {
		if clientIP, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
			// If we aren't the first proxy retain prior
			// X-Forwarded-For information as a comma+space
			// separated list and fold multiple headers into one.
			prior, ok := outreq.Header["X-Forwarded-For"]
			omit := ok && prior == nil // Issue 38079: nil now means don't populate the header
			if len(prior) > 0 {
				clientIP = strings.Join(prior, ", ") + ", " + clientIP
			}
			if !omit {
				outreq.Header.Set("X-Forwarded-For", clientIP)
			}
		}
	}

	if _, ok := outreq.Header["User-Agent"]; !ok {
		// If the outbound request doesn't have a User-Agent header set,
		// don't send the default Go HTTP client User-Agent.
		outreq.Header.Set("User-Agent", "")
	}

	var (
		roundTripMutex sync.Mutex
		roundTripDone  bool
	)
	trace := &httptrace.ClientTrace{
		Got1xxResponse: func(code int, header textproto.MIMEHeader) error {
			roundTripMutex.Lock()
			defer roundTripMutex.Unlock()
			if roundTripDone {
				// If RoundTrip has returned, don't try to further modify
				// the ResponseWriter's header map.
				return nil
			}
			h := rw.Header()
			copyHeader(h, http.Header(header))
			rw.WriteHeader(code)

			// Clear headers, it's not automatically done by ResponseWriter.WriteHeader() for 1xx responses
			clear(h)
			return nil
		},
	}
	outreq = outreq.WithContext(httptrace.WithClientTrace(outreq.Context(), trace))

	res, err := transport.RoundTrip(outreq)
	roundTripMutex.Lock()
	roundTripDone = true
	roundTripMutex.Unlock()
	if err != nil {
		p.getErrorHandler()(rw, outreq, err)
		return
	}
	if res.StatusCode == 200 {
		sb := p.modifyResponsePre(rw, res, outreq)
		if sb == nil {
			traceLlmNotStreamResponse(res)
		} else {
			defer sb.Close()
		}
	} else {
		statusCode := res.StatusCode
		fmt.Printf("HTTP Status Code: %v, 本轮不记录llm响应", statusCode)
	}

	// Deal with 101 Switching Protocols responses: (WebSocket, h2c, etc)
	if res.StatusCode == http.StatusSwitchingProtocols {
		if !p.modifyResponse(rw, res, outreq) {
			return
		}
		p.handleUpgradeResponse(rw, outreq, res)
		return
	}

	removeHopByHopHeaders(res.Header)

	if !p.modifyResponse(rw, res, outreq) {
		return
	}

	copyHeader(rw.Header(), res.Header)

	// The "Trailer" header isn't included in the Transport's response,
	// at least for *http.Transport. Build it up from Trailer.
	announcedTrailers := len(res.Trailer)
	if announcedTrailers > 0 {
		trailerKeys := make([]string, 0, len(res.Trailer))
		for k := range res.Trailer {
			trailerKeys = append(trailerKeys, k)
		}
		rw.Header().Add("Trailer", strings.Join(trailerKeys, ", "))
	}

	rw.WriteHeader(res.StatusCode)

	err = p.copyResponse(rw, res.Body, p.flushInterval(res))
	if err != nil {
		defer res.Body.Close()
		// Since we're streaming the response, if we run into an error all we can do
		// is abort the request. Issue 23643: ReverseProxy should use ErrAbortHandler
		// on read error while copying body.
		if !shouldPanicOnCopyError(req) {
			p.logf("suppressing panic for copyResponse error in test; copy error: %v", err)
			return
		}
		panic(http.ErrAbortHandler)
	}
	res.Body.Close() // close now, instead of defer, to populate res.Trailer

	if len(res.Trailer) > 0 {
		// Force chunking if we saw a response trailer.
		// This prevents net/http from calculating the length for short
		// bodies and adding a Content-Length.
		http.NewResponseController(rw).Flush()
	}

	if len(res.Trailer) == announcedTrailers {
		copyHeader(rw.Header(), res.Trailer)
		return
	}

	for k, vv := range res.Trailer {
		k = http.TrailerPrefix + k
		for _, v := range vv {
			rw.Header().Add(k, v)
		}
	}

}

var inOurTests bool // whether we're in our own tests

// shouldPanicOnCopyError reports whether the reverse proxy should
// panic with http.ErrAbortHandler. This is the right thing to do by
// default, but Go 1.10 and earlier did not, so existing unit tests
// weren't expecting panics. Only panic in our own tests, or when
// running under the HTTP server.
func shouldPanicOnCopyError(req *http.Request) bool {
	if inOurTests {
		// Our tests know to handle this panic.
		return true
	}
	if req.Context().Value(http.ServerContextKey) != nil {
		// We seem to be running under an HTTP server, so
		// it'll recover the panic.
		return true
	}
	// Otherwise act like Go 1.10 and earlier to not break
	// existing tests.
	return false
}

// removeHopByHopHeaders removes hop-by-hop headers.
func removeHopByHopHeaders(h http.Header) {
	// RFC 7230, section 6.1: Remove headers listed in the "Connection" header.
	for _, f := range h["Connection"] {
		for _, sf := range strings.Split(f, ",") {
			if sf = textproto.TrimString(sf); sf != "" {
				h.Del(sf)
			}
		}
	}
	// RFC 2616, section 13.5.1: Remove a set of known hop-by-hop headers.
	// This behavior is superseded by the RFC 7230 Connection header, but
	// preserve it for backwards compatibility.
	for _, f := range hopHeaders {
		h.Del(f)
	}
}

// flushInterval returns the p.FlushInterval value, conditionally
// overriding its value for a specific request/response.
func (p *ReverseProxy) flushInterval(res *http.Response) time.Duration {
	resCT := res.Header.Get("Content-Type")

	// For Server-Sent Events responses, flush immediately.
	// The MIME type is defined in https://www.w3.org/TR/eventsource/#text-event-stream
	if baseCT, _, _ := mime.ParseMediaType(resCT); baseCT == "text/event-stream" {
		return -1 // negative means immediately
	}

	// We might have the case of streaming for which Content-Length might be unset.
	if res.ContentLength == -1 {
		return -1
	}

	return p.FlushInterval
}

func (p *ReverseProxy) copyResponse(dst http.ResponseWriter, src io.Reader, flushInterval time.Duration) error {
	var w io.Writer = dst

	if flushInterval != 0 {
		mlw := &maxLatencyWriter{
			dst:     dst,
			flush:   http.NewResponseController(dst).Flush,
			latency: flushInterval,
		}
		defer mlw.stop()

		// set up initial timer so headers get flushed even if body writes are delayed
		mlw.flushPending = true
		mlw.t = time.AfterFunc(flushInterval, mlw.delayedFlush)

		w = mlw
	}

	var buf []byte
	if p.BufferPool != nil {
		buf = p.BufferPool.Get()
		defer p.BufferPool.Put(buf)
	}
	_, err := p.copyBuffer(w, src, buf)
	return err
}

// copyBuffer returns any write errors or non-EOF read errors, and the amount
// of bytes written.
func (p *ReverseProxy) copyBuffer(dst io.Writer, src io.Reader, buf []byte) (int64, error) {
	if len(buf) == 0 {
		buf = make([]byte, 32*1024)
	}
	var written int64
	for {
		nr, rerr := src.Read(buf)
		if rerr != nil && rerr != io.EOF && rerr != context.Canceled {
			p.logf("httputil: ReverseProxy read error during body copy: %v", rerr)
		}
		if nr > 0 {
			nw, werr := dst.Write(buf[:nr])
			if nw > 0 {
				written += int64(nw)
			}
			if werr != nil {
				return written, werr
			}
			if nr != nw {
				return written, io.ErrShortWrite
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				rerr = nil
			}
			return written, rerr
		}
	}
}

func (p *ReverseProxy) logf(format string, args ...any) {
	if p.ErrorLog != nil {
		p.ErrorLog.Printf(format, args...)
	} else {
		log.Printf(format, args...)
	}
}

type maxLatencyWriter struct {
	dst     io.Writer
	flush   func() error
	latency time.Duration // non-zero; negative means to flush immediately

	mu           sync.Mutex // protects t, flushPending, and dst.Flush
	t            *time.Timer
	flushPending bool
}

func (m *maxLatencyWriter) Write(p []byte) (n int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, err = m.dst.Write(p)
	if m.latency < 0 {
		m.flush()
		return
	}
	if m.flushPending {
		return
	}
	if m.t == nil {
		m.t = time.AfterFunc(m.latency, m.delayedFlush)
	} else {
		m.t.Reset(m.latency)
	}
	m.flushPending = true
	return
}

func (m *maxLatencyWriter) delayedFlush() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.flushPending { // if stop was called but AfterFunc already started this goroutine
		return
	}
	m.flush()
	m.flushPending = false
}

func (m *maxLatencyWriter) stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.flushPending = false
	if m.t != nil {
		m.t.Stop()
	}
}

func upgradeType(h http.Header) string {
	if !httpguts.HeaderValuesContainsToken(h["Connection"], "Upgrade") {
		return ""
	}
	return h.Get("Upgrade")
}

func (p *ReverseProxy) handleUpgradeResponse(rw http.ResponseWriter, req *http.Request, res *http.Response) {
	reqUpType := upgradeType(req.Header)
	resUpType := upgradeType(res.Header)
	if !IsPrint(resUpType) { // We know reqUpType is ASCII, it's checked by the caller.
		p.getErrorHandler()(rw, req, fmt.Errorf("backend tried to switch to invalid protocol %q", resUpType))
	}
	if !EqualFold(reqUpType, resUpType) {
		p.getErrorHandler()(rw, req, fmt.Errorf("backend tried to switch protocol %q when %q was requested", resUpType, reqUpType))
		return
	}

	backConn, ok := res.Body.(io.ReadWriteCloser)
	if !ok {
		p.getErrorHandler()(rw, req, fmt.Errorf("internal error: 101 switching protocols response with non-writable body"))
		return
	}

	rc := http.NewResponseController(rw)
	conn, brw, hijackErr := rc.Hijack()
	if errors.Is(hijackErr, http.ErrNotSupported) {
		p.getErrorHandler()(rw, req, fmt.Errorf("can't switch protocols using non-Hijacker ResponseWriter type %T", rw))
		return
	}

	backConnCloseCh := make(chan bool)
	go func() {
		// Ensure that the cancellation of a request closes the backend.
		// See issue https://golang.org/issue/35559.
		select {
		case <-req.Context().Done():
		case <-backConnCloseCh:
		}
		backConn.Close()
	}()
	defer close(backConnCloseCh)

	if hijackErr != nil {
		p.getErrorHandler()(rw, req, fmt.Errorf("Hijack failed on protocol switch: %v", hijackErr))
		return
	}
	defer conn.Close()

	copyHeader(rw.Header(), res.Header)

	res.Header = rw.Header()
	res.Body = nil // so res.Write only writes the headers; we have res.Body in backConn above
	if err := res.Write(brw); err != nil {
		p.getErrorHandler()(rw, req, fmt.Errorf("response write: %v", err))
		return
	}
	if err := brw.Flush(); err != nil {
		p.getErrorHandler()(rw, req, fmt.Errorf("response flush: %v", err))
		return
	}
	errc := make(chan error, 1)
	spc := switchProtocolCopier{user: conn, backend: backConn}
	go spc.copyToBackend(errc)
	go spc.copyFromBackend(errc)
	<-errc
}

// switchProtocolCopier exists so goroutines proxying data back and
// forth have nice names in stacks.
type switchProtocolCopier struct {
	user, backend io.ReadWriter
}

func (c switchProtocolCopier) copyFromBackend(errc chan<- error) {
	_, err := io.Copy(c.user, c.backend)
	errc <- err
}

func (c switchProtocolCopier) copyToBackend(errc chan<- error) {
	_, err := io.Copy(c.backend, c.user)
	errc <- err
}

func cleanQueryParams(s string) string {
	reencode := func(s string) string {
		v, _ := url.ParseQuery(s)
		return v.Encode()
	}
	for i := 0; i < len(s); {
		switch s[i] {
		case ';':
			return reencode(s)
		case '%':
			if i+2 >= len(s) || !ishex(s[i+1]) || !ishex(s[i+2]) {
				return reencode(s)
			}
			i += 3
		default:
			i++
		}
	}
	return s
}

func ishex(c byte) bool {
	switch {
	case '0' <= c && c <= '9':
		return true
	case 'a' <= c && c <= 'f':
		return true
	case 'A' <= c && c <= 'F':
		return true
	}
	return false
}

// EqualFold is [strings.EqualFold], ASCII only. It reports whether s and t
// are equal, ASCII-case-insensitively.
func EqualFold(s, t string) bool {
	if len(s) != len(t) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if lower(s[i]) != lower(t[i]) {
			return false
		}
	}
	return true
}

// lower returns the ASCII lowercase version of b.
func lower(b byte) byte {
	if 'A' <= b && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

// IsPrint returns whether s is ASCII and printable according to
// https://tools.ietf.org/html/rfc20#section-4.2.
func IsPrint(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < ' ' || s[i] > '~' {
			return false
		}
	}
	return true
}

// Is returns whether s is ASCII.
func Is(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > unicode.MaxASCII {
			return false
		}
	}
	return true
}

// ToLower returns the lowercase version of s if s is ASCII and printable.
func ToLower(s string) (lower string, ok bool) {
	if !IsPrint(s) {
		return "", false
	}
	return strings.ToLower(s), true
}
