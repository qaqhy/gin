// Copyright 2014 Manu Martinez-Almeida. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package gin

import (
	"errors"
	"io"
	"io/fs"
	"log"
	"math"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-contrib/sse"
	"github.com/gin-gonic/gin/binding"
	"github.com/gin-gonic/gin/render"
)

// 最常见数据格式的 Content-Type MIME。
const (
	MIMEJSON              = binding.MIMEJSON
	MIMEHTML              = binding.MIMEHTML
	MIMEXML               = binding.MIMEXML
	MIMEXML2              = binding.MIMEXML2
	MIMEPlain             = binding.MIMEPlain
	MIMEPOSTForm          = binding.MIMEPOSTForm
	MIMEMultipartPOSTForm = binding.MIMEMultipartPOSTForm
	MIMEYAML              = binding.MIMEYAML
	MIMEYAML2             = binding.MIMEYAML2
	MIMETOML              = binding.MIMETOML
)

// BodyBytesKey 表示一个默认的 body 字节 key。
const BodyBytesKey = "_gin-gonic/gin/bodybyteskey"

// ContextKey 是 Context 返回自身的 key。
const ContextKey = "_gin-gonic/gin/contextkey"

type ContextKeyType int

const ContextRequestKey ContextKeyType = 0

// abortIndex 表示在 abort 函数中使用的典型值。
const abortIndex int8 = math.MaxInt8 >> 1

// Context 是 gin 中最重要的部分。它允许我们在中间件之间传递变量，管理流程，验证请求的 JSON 并渲染 JSON 响应，例如。
type Context struct {
	writermem responseWriter
	Request   *http.Request
	Writer    ResponseWriter

	Params   Params
	handlers HandlersChain
	index    int8
	fullPath string

	engine       *Engine
	params       *Params
	skippedNodes *[]skippedNode

	// This mutex protects Keys map.
	mu sync.RWMutex

	// Keys 是专门用于每个请求上下文的键/值对。
	Keys map[string]any

	// Errors 是附加到使用此上下文的所有处理程序/中间件的错误列表。
	Errors errorMsgs

	// Accepted 定义了内容协商中手动接受的格式列表。
	Accepted []string

	// queryCache 缓存了从 c.Request.URL.Query() 获取的查询结果。
	queryCache url.Values

	// formCache 缓存了 c.Request.PostForm，其中包含从 POST、PATCH 或 PUT 请求体参数解析的表单数据。
	formCache url.Values

	// SameSite 允许服务器定义一个 cookie 属性，使得浏览器在跨站请求时无法发送此 cookie。
	sameSite http.SameSite
}

/************************************/
/********** CONTEXT CREATION ********/
/************************************/

func (c *Context) reset() {
	c.Writer = &c.writermem
	c.Params = c.Params[:0]
	c.handlers = nil
	c.index = -1

	c.fullPath = ""
	c.Keys = nil
	c.Errors = c.Errors[:0]
	c.Accepted = nil
	c.queryCache = nil
	c.formCache = nil
	c.sameSite = 0
	*c.params = (*c.params)[:0]
	*c.skippedNodes = (*c.skippedNodes)[:0]
}

// Copy 返回当前上下文的副本，该副本可以在请求范围之外安全使用。
// 当需要将上下文传递给一个 goroutine 时，必须使用这个方法。
func (c *Context) Copy() *Context {
	cp := Context{
		writermem: c.writermem,
		Request:   c.Request,
		engine:    c.engine,
	}

	cp.writermem.ResponseWriter = nil
	cp.Writer = &cp.writermem
	cp.index = abortIndex
	cp.handlers = nil
	cp.fullPath = c.fullPath

	cKeys := c.Keys
	cp.Keys = make(map[string]any, len(cKeys))
	c.mu.RLock()
	for k, v := range cKeys {
		cp.Keys[k] = v
	}
	c.mu.RUnlock()

	cParams := c.Params
	cp.Params = make([]Param, len(cParams))
	copy(cp.Params, cParams)

	return &cp
}

// HandlerName 返回主处理器的名称。例如，如果处理器是 "handleGetUsers()",
// 这个函数将返回 "main.handleGetUsers"。
func (c *Context) HandlerName() string {
	return nameOfFunction(c.handlers.Last())
}

// HandlerNames 返回按降序排列的所有为此上下文注册的处理器的列表，
// 遵循 HandlerName() 的语义
func (c *Context) HandlerNames() []string {
	hn := make([]string, 0, len(c.handlers))
	for _, val := range c.handlers {
		if val == nil {
			continue
		}
		hn = append(hn, nameOfFunction(val))
	}
	return hn
}

// Handler 返回主处理器。
func (c *Context) Handler() HandlerFunc {
	return c.handlers.Last()
}

// FullPath 返回匹配的路由完整路径。对于未找到的路由，返回空字符串。
//
//	router.GET("/user/:id", func(c *gin.Context) {
//	    c.FullPath() == "/user/:id" // true
//	})
func (c *Context) FullPath() string {
	return c.fullPath
}

//************************************/
//*********** 流程控制 ***********/
//************************************/

// Next 只能在中间件内部使用。
// 它在调用的处理器内部执行链中的待处理处理器。
// 请参阅 GitHub 上的示例。
func (c *Context) Next() {
	c.index++
	for c.index < int8(len(c.handlers)) {
		if c.handlers[c.index] != nil {
			c.handlers[c.index](c)
		}
		c.index++
	}
}

// IsAborted 如果当前上下文被中止，返回 true。
func (c *Context) IsAborted() bool {
	return c.index >= abortIndex
}

// Abort 阻止调用待处理的处理器。请注意，这不会停止当前的处理器。
// 假设你有一个验证当前请求是否被授权的授权中间件。
// 如果授权失败（例如：密码不匹配），调用 Abort 以确保不会调用此请求的剩余处理器。
func (c *Context) Abort() {
	c.index = abortIndex
}

// AbortWithStatus 调用 `Abort()` 并用指定的状态码写入头部信息。
// 例如，尝试验证请求失败可以使用：context.AbortWithStatus(401)。
func (c *Context) AbortWithStatus(code int) {
	c.Status(code)
	c.Writer.WriteHeaderNow()
	c.Abort()
}

// AbortWithStatusJSON 内部调用 `Abort()` 然后调用 `JSON`。
// 该方法停止处理链，写入状态码并返回 JSON 响应体。
// 它还将 Content-Type 设置为 "application/json"。
func (c *Context) AbortWithStatusJSON(code int, jsonObj any) {
	c.Abort()
	c.JSON(code, jsonObj)
}

// AbortWithError 内部调用 `AbortWithStatus()` 和 `Error()`。
// 该方法停止处理链，写入状态码并将指定的错误推送到 `c.Errors`。
// 有关更多详细信息，请参见 Context.Error()。
func (c *Context) AbortWithError(code int, err error) *Error {
	c.AbortWithStatus(code)
	return c.Error(err)
}

//************************************/
//************** 错误管理 *************/
//************************************/

// Error 将一个错误附加到当前上下文。该错误被推送到错误列表中。
// 在请求处理过程中发生的每个错误都调用 Error 是一个好主意。
// 可以使用中间件来收集所有错误并将它们一起推送到数据库中，打印日志，或附加到 HTTP 响应中。
// 如果 err 为 nil，Error 将会引发 panic。
func (c *Context) Error(err error) *Error {
	if err == nil {
		panic("err is nil")
	}

	var parsedError *Error
	ok := errors.As(err, &parsedError)
	if !ok {
		parsedError = &Error{
			Err:  err,
			Type: ErrorTypePrivate,
		}
	}

	c.Errors = append(c.Errors, parsedError)
	return parsedError
}

//************************************/
//******** 元数据管理 ********/
//************************************/

// Set 用于存储一个专用于该上下文的新键/值对。
// 如果 c.Keys 之前没有使用过，它也会懒初始化 c.Keys。
func (c *Context) Set(key string, value any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Keys == nil {
		c.Keys = make(map[string]any)
	}

	c.Keys[key] = value
}

// Get 返回给定键的值，即： (value, true)。
// 如果值不存在，则返回 (nil, false)。
func (c *Context) Get(key string) (value any, exists bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	value, exists = c.Keys[key]
	return
}

// MustGet 返回给定键的值（如果存在），否则会引发 panic。
func (c *Context) MustGet(key string) any {
	if value, exists := c.Get(key); exists {
		return value
	}
	panic("Key \"" + key + "\" does not exist")
}

func getTyped[T any](c *Context, key string) (res T) {
	if val, ok := c.Get(key); ok && val != nil {
		res, _ = val.(T)
	}
	return
}

// GetString 返回与键关联的值作为字符串。
func (c *Context) GetString(key string) (s string) {
	return getTyped[string](c, key)
}

// GetBool 返回与键关联的值作为布尔值。
func (c *Context) GetBool(key string) (b bool) {
	return getTyped[bool](c, key)
}

// GetInt 返回与键关联的值作为整数。
func (c *Context) GetInt(key string) (i int) {
	return getTyped[int](c, key)
}

// GetInt8 返回与键关联的值作为 8 位整数。
func (c *Context) GetInt8(key string) (i8 int8) {
	return getTyped[int8](c, key)
}

// GetInt16 返回与键关联的值作为 16 位整数。
func (c *Context) GetInt16(key string) (i16 int16) {
	return getTyped[int16](c, key)
}

// GetInt32 返回与键关联的值作为 32 位整数。
func (c *Context) GetInt32(key string) (i32 int32) {
	return getTyped[int32](c, key)
}

// GetInt64 返回与键关联的值作为 64 位整数。
func (c *Context) GetInt64(key string) (i64 int64) {
	return getTyped[int64](c, key)
}

// GetUint 返回与键关联的值作为无符号整数。
func (c *Context) GetUint(key string) (ui uint) {
	return getTyped[uint](c, key)
}

// GetUint8 返回与键关联的值作为 8 位无符号整数。
func (c *Context) GetUint8(key string) (ui8 uint8) {
	return getTyped[uint8](c, key)
}

// GetUint16 返回与键关联的值作为 16 位无符号整数。
func (c *Context) GetUint16(key string) (ui16 uint16) {
	return getTyped[uint16](c, key)
}

// GetUint32 返回与键关联的值作为 32 位无符号整数。
func (c *Context) GetUint32(key string) (ui32 uint32) {
	return getTyped[uint32](c, key)
}

// GetUint64 返回与键关联的值作为 64 位无符号整数。
func (c *Context) GetUint64(key string) (ui64 uint64) {
	return getTyped[uint64](c, key)
}

// GetFloat32 返回与键关联的值作为 float32。
func (c *Context) GetFloat32(key string) (f32 float32) {
	return getTyped[float32](c, key)
}

// GetFloat64 返回与键关联的值作为 float64。
func (c *Context) GetFloat64(key string) (f64 float64) {
	return getTyped[float64](c, key)
}

// GetTime 返回与键关联的值作为时间。
func (c *Context) GetTime(key string) (t time.Time) {
	return getTyped[time.Time](c, key)
}

// GetDuration 返回与键关联的值作为持续时间。
func (c *Context) GetDuration(key string) (d time.Duration) {
	return getTyped[time.Duration](c, key)
}

// GetIntSlice 返回与键关联的值作为整数切片。
func (c *Context) GetIntSlice(key string) (is []int) {
	return getTyped[[]int](c, key)
}

// GetInt8Slice 返回与键关联的值作为 int8 整数切片。
func (c *Context) GetInt8Slice(key string) (i8s []int8) {
	return getTyped[[]int8](c, key)
}

// GetInt16Slice 返回与键关联的值作为 int16 整数切片。
func (c *Context) GetInt16Slice(key string) (i16s []int16) {
	return getTyped[[]int16](c, key)
}

// GetInt32Slice 返回与键关联的值作为 int32 整数切片。
func (c *Context) GetInt32Slice(key string) (i32s []int32) {
	return getTyped[[]int32](c, key)
}

// GetInt64Slice 返回与键关联的值作为 int64 整数切片。
func (c *Context) GetInt64Slice(key string) (i64s []int64) {
	return getTyped[[]int64](c, key)
}

// GetUintSlice 返回与键关联的值作为无符号整数切片。
func (c *Context) GetUintSlice(key string) (uis []uint) {
	return getTyped[[]uint](c, key)
}

// GetUint8Slice 返回与键关联的值作为 uint8 整数切片。
func (c *Context) GetUint8Slice(key string) (ui8s []uint8) {
	return getTyped[[]uint8](c, key)
}

// GetUint16Slice 返回与键关联的值，作为 uint16 整数的切片。
func (c *Context) GetUint16Slice(key string) (ui16s []uint16) {
	return getTyped[[]uint16](c, key)
}

// GetUint32Slice 返回与键关联的值，作为 uint32 整数的切片。
func (c *Context) GetUint32Slice(key string) (ui32s []uint32) {
	return getTyped[[]uint32](c, key)
}

// GetUint64Slice 返回与键关联的值，作为 uint64 整数的切片。
func (c *Context) GetUint64Slice(key string) (ui64s []uint64) {
	return getTyped[[]uint64](c, key)
}

// GetFloat32Slice 返回与键关联的值，作为 float32 数字的切片。
func (c *Context) GetFloat32Slice(key string) (f32s []float32) {
	return getTyped[[]float32](c, key)
}

// GetFloat64Slice 返回与键关联的值，作为 float64 数字的切片。
func (c *Context) GetFloat64Slice(key string) (f64s []float64) {
	return getTyped[[]float64](c, key)
}

// GetStringSlice 返回与键关联的值，作为字符串的切片。
func (c *Context) GetStringSlice(key string) (ss []string) {
	return getTyped[[]string](c, key)
}

// GetStringMap 返回与键关联的值，作为接口的映射。
func (c *Context) GetStringMap(key string) (sm map[string]any) {
	return getTyped[map[string]any](c, key)
}

// GetStringMapString 返回与键关联的值，作为字符串的映射。
func (c *Context) GetStringMapString(key string) (sms map[string]string) {
	return getTyped[map[string]string](c, key)
}

// GetStringMapStringSlice 返回与键关联的值，作为指向字符串切片的映射。
func (c *Context) GetStringMapStringSlice(key string) (smss map[string][]string) {
	return getTyped[map[string][]string](c, key)
}

/************************************/
/************* 输入数据 **************/
/************************************/

// Param 返回 URL 参数的值。
// 它是 c.Params.ByName(key) 的快捷方式
//
//	router.GET("/user/:id", func(c *gin.Context) {
//	    // 一个 GET 请求到 /user/john
//	    id := c.Param("id") // id == "john"
//	    // 一个 GET 请求到 /user/john/
//	    id := c.Param("id") // id == "/john/"
//	})
func (c *Context) Param(key string) string {
	return c.Params.ByName(key)
}

// AddParam 向上下文添加参数并
// 将路径参数键替换为给定值，用于端到端测试
// 示例路由: "/user/:id"
// AddParam("id", 1)
// Result: "/user/1"
func (c *Context) AddParam(key, value string) {
	c.Params = append(c.Params, Param{Key: key, Value: value})
}

// Query 返回键控 URL 查询值（如果存在），
// 否则返回空字符串 `("")`。
// 它是 `c.Request.URL.Query().Get(key)` 的快捷方式
//
//	    GET /path?id=1234&name=Manu&value=
//		   c.Query("id") == "1234"
//		   c.Query("name") == "Manu"
//		   c.Query("value") == ""
//		   c.Query("wtf") == ""
func (c *Context) Query(key string) (value string) {
	value, _ = c.GetQuery(key)
	return
}

// DefaultQuery 返回键控 URL 查询值（如果存在），
// 否则返回指定的 defaultValue 字符串。
// 参见: Query() 和 GetQuery() 获取更多信息。
//
//	GET /?name=Manu&lastname=
//	c.DefaultQuery("name", "unknown") == "Manu"
//	c.DefaultQuery("id", "none") == "none"
//	c.DefaultQuery("lastname", "none") == ""
func (c *Context) DefaultQuery(key, defaultValue string) string {
	if value, ok := c.GetQuery(key); ok {
		return value
	}
	return defaultValue
}

// GetQuery 类似于 Query()，它返回键控 URL 查询值
// 如果存在 `(value, true)`（即使值为空字符串），
// 否则返回 `("", false)`。
// 它是 `c.Request.URL.Query().Get(key)` 的快捷方式
//
//	GET /?name=Manu&lastname=
//	("Manu", true) == c.GetQuery("name")
//	("", false) == c.GetQuery("id")
//	("", true) == c.GetQuery("lastname")
func (c *Context) GetQuery(key string) (string, bool) {
	if values, ok := c.GetQueryArray(key); ok {
		return values[0], ok
	}
	return "", false
}

// QueryArray 返回给定查询键的字符串切片。
// 切片的长度取决于具有给定键的参数数量。
func (c *Context) QueryArray(key string) (values []string) {
	values, _ = c.GetQueryArray(key)
	return
}

func (c *Context) initQueryCache() {
	if c.queryCache == nil {
		if c.Request != nil && c.Request.URL != nil {
			c.queryCache = c.Request.URL.Query()
		} else {
			c.queryCache = url.Values{}
		}
	}
}

// GetQueryArray 返回给定查询键的字符串切片，以及
// 一个布尔值，表示是否至少存在一个给定键的值。
func (c *Context) GetQueryArray(key string) (values []string, ok bool) {
	c.initQueryCache()
	values, ok = c.queryCache[key]
	return
}

// QueryMap 返回给定查询键的映射。
func (c *Context) QueryMap(key string) (dicts map[string]string) {
	dicts, _ = c.GetQueryMap(key)
	return
}

// GetQueryMap 返回给定查询键的映射，以及一个布尔值
// 表示是否至少存在一个给定键的值。
func (c *Context) GetQueryMap(key string) (map[string]string, bool) {
	c.initQueryCache()
	return c.get(c.queryCache, key)
}

// PostForm 返回 POST urlencoded 表单或 multipart 表单中指定键的值
// 当它存在时，否则返回空字符串 `("")`。
func (c *Context) PostForm(key string) (value string) {
	value, _ = c.GetPostForm(key)
	return
}

// DefaultPostForm 返回 POST urlencoded 表单或 multipart 表单中指定键的值
// 当它存在时，否则返回指定的 defaultValue 字符串。
// 参见: PostForm() 和 GetPostForm() 获取更多信息。
func (c *Context) DefaultPostForm(key, defaultValue string) string {
	if value, ok := c.GetPostForm(key); ok {
		return value
	}
	return defaultValue
}

// GetPostForm 类似于 PostForm(key)。它返回 POST urlencoded
// 表单或 multipart 表单中指定键的值，当它存在时 `(value, true)`（即使值为空字符串），
// 否则返回 ("", false)。
// 例如，在 PATCH 请求中更新用户的电子邮件：
//
//	    email=mail@example.com  -->  ("mail@example.com", true) := GetPostForm("email") // 将 email 设置为 "mail@example.com"
//		   email=                  -->  ("", true) := GetPostForm("email") // 将 email 设置为空字符串 ""
//	                            -->  ("", false) := GetPostForm("email") // 不对 email 进行任何操作
func (c *Context) GetPostForm(key string) (string, bool) {
	if values, ok := c.GetPostFormArray(key); ok {
		return values[0], ok
	}
	return "", false
}

// PostFormArray 返回给定表单键的字符串切片。
// 切片的长度取决于具有给定键的参数数量。
func (c *Context) PostFormArray(key string) (values []string) {
	values, _ = c.GetPostFormArray(key)
	return
}

func (c *Context) initFormCache() {
	if c.formCache == nil {
		c.formCache = make(url.Values)
		req := c.Request
		if err := req.ParseMultipartForm(c.engine.MaxMultipartMemory); err != nil {
			if !errors.Is(err, http.ErrNotMultipart) {
				debugPrint("解析 multipart 表单数组时出错: %v", err)
			}
		}
		c.formCache = req.PostForm
	}
}

// GetPostFormArray 返回给定表单键的字符串切片，以及
// 一个布尔值，表示是否至少存在一个给定键的值。
func (c *Context) GetPostFormArray(key string) (values []string, ok bool) {
	c.initFormCache()
	values, ok = c.formCache[key]
	return
}

// PostFormMap 返回给定表单键的映射。
func (c *Context) PostFormMap(key string) (dicts map[string]string) {
	dicts, _ = c.GetPostFormMap(key)
	return
}

// GetPostFormMap 返回给定表单键的映射，以及一个布尔值
// 表示是否至少存在一个给定键的值。
func (c *Context) GetPostFormMap(key string) (map[string]string, bool) {
	c.initFormCache()
	return c.get(c.formCache, key)
}

// get 是一个内部方法，返回满足条件的映射。
func (c *Context) get(m map[string][]string, key string) (map[string]string, bool) {
	dicts := make(map[string]string)
	exist := false
	for k, v := range m {
		if i := strings.IndexByte(k, '['); i >= 1 && k[0:i] == key {
			if j := strings.IndexByte(k[i+1:], ']'); j >= 1 {
				exist = true
				dicts[k[i+1:][:j]] = v[0]
			}
		}
	}
	return dicts, exist
}

// FormFile 返回提供的表单键的第一个文件。
func (c *Context) FormFile(name string) (*multipart.FileHeader, error) {
	if c.Request.MultipartForm == nil {
		if err := c.Request.ParseMultipartForm(c.engine.MaxMultipartMemory); err != nil {
			return nil, err
		}
	}
	f, fh, err := c.Request.FormFile(name)
	if err != nil {
		return nil, err
	}
	f.Close()
	return fh, err
}

// MultipartForm 是解析后的 multipart 表单，包括文件上传。
func (c *Context) MultipartForm() (*multipart.Form, error) {
	err := c.Request.ParseMultipartForm(c.engine.MaxMultipartMemory)
	return c.Request.MultipartForm, err
}

// SaveUploadedFile 将表单文件上传到指定的目标路径。
func (c *Context) SaveUploadedFile(file *multipart.FileHeader, dst string, perm ...fs.FileMode) error {
	src, err := file.Open()
	if err != nil {
		return err
	}
	defer src.Close()

	if len(perm) <= 0 {
		perm = append(perm, 0o750)
	}

	if err = os.MkdirAll(filepath.Dir(dst), perm[0]); err != nil {
		return err
	}

	if err = os.Chmod(filepath.Dir(dst), perm[0]); err != nil {
		return err
	}

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, src)
	return err
}

// Bind 检查 Method 和 Content-Type 以自动选择绑定引擎，
// 根据 "Content-Type" 头使用不同的绑定，例如：
//
//	"application/json" --> JSON 绑定
//	"application/xml"  --> XML 绑定
//
// 如果 Content-Type == "application/json"，它将请求的 body 解析为 JSON，使用 JSON 或 XML 作为 JSON 输入。
// 它将 json 负载解码到指定的结构体指针中。
// 如果输入无效，它会写入 400 错误并在响应中设置 Content-Type 头为 "text/plain"。
func (c *Context) Bind(obj any) error {
	b := binding.Default(c.Request.Method, c.ContentType())
	return c.MustBindWith(obj, b)
}

// BindJSON 是 c.MustBindWith(obj, binding.JSON) 的快捷方式。
func (c *Context) BindJSON(obj any) error {
	return c.MustBindWith(obj, binding.JSON)
}

// BindXML 是 c.MustBindWith(obj, binding.XML) 的快捷方式。
func (c *Context) BindXML(obj any) error {
	return c.MustBindWith(obj, binding.XML)
}

// BindQuery 是 c.MustBindWith(obj, binding.Query) 的快捷方式。
func (c *Context) BindQuery(obj any) error {
	return c.MustBindWith(obj, binding.Query)
}

// BindYAML 是 c.MustBindWith(obj, binding.YAML) 的快捷方式。
func (c *Context) BindYAML(obj any) error {
	return c.MustBindWith(obj, binding.YAML)
}

// BindTOML 是 c.MustBindWith(obj, binding.TOML) 的快捷方式。
func (c *Context) BindTOML(obj any) error {
	return c.MustBindWith(obj, binding.TOML)
}

// BindPlain 是 c.MustBindWith(obj, binding.Plain) 的快捷方式。
func (c *Context) BindPlain(obj any) error {
	return c.MustBindWith(obj, binding.Plain)
}

// BindHeader 是 c.MustBindWith(obj, binding.Header) 的快捷方式。
func (c *Context) BindHeader(obj any) error {
	return c.MustBindWith(obj, binding.Header)
}

// BindUri 使用 binding.Uri 绑定传递的结构体指针。
// 如果发生任何错误，它将以 HTTP 400 中止请求。
func (c *Context) BindUri(obj any) error {
	if err := c.ShouldBindUri(obj); err != nil {
		c.AbortWithError(http.StatusBadRequest, err).SetType(ErrorTypeBind) //nolint: errcheck
		return err
	}
	return nil
}

// MustBindWith 使用指定的绑定引擎绑定传递的结构体指针。
// 如果发生任何错误，它将以 HTTP 400 中止请求。
// 详见 binding 包。
func (c *Context) MustBindWith(obj any, b binding.Binding) error {
	if err := c.ShouldBindWith(obj, b); err != nil {
		c.AbortWithError(http.StatusBadRequest, err).SetType(ErrorTypeBind) //nolint: errcheck
		return err
	}
	return nil
}

// ShouldBind 检查 Method 和 Content-Type 以自动选择绑定引擎，
// 根据 "Content-Type" 头使用不同的绑定，例如：
//
//	"application/json" --> JSON 绑定
//	"application/xml"  --> XML 绑定
//
// 如果 Content-Type == "application/json"，它将请求的 body 解析为 JSON，使用 JSON 或 XML 作为 JSON 输入。
// 它将 json 负载解码到指定的结构体指针中。
// 类似于 c.Bind()，但此方法不会将响应状态码设置为 400 或在输入无效时中止。
func (c *Context) ShouldBind(obj any) error {
	b := binding.Default(c.Request.Method, c.ContentType())
	return c.ShouldBindWith(obj, b)
}

// ShouldBindJSON 是 c.ShouldBindWith(obj, binding.JSON) 的快捷方式。
func (c *Context) ShouldBindJSON(obj any) error {
	return c.ShouldBindWith(obj, binding.JSON)
}

// ShouldBindXML 是 c.ShouldBindWith(obj, binding.XML) 的快捷方式。
func (c *Context) ShouldBindXML(obj any) error {
	return c.ShouldBindWith(obj, binding.XML)
}

// ShouldBindQuery 是 c.ShouldBindWith(obj, binding.Query) 的快捷方式。
func (c *Context) ShouldBindQuery(obj any) error {
	return c.ShouldBindWith(obj, binding.Query)
}

// ShouldBindYAML 是 c.ShouldBindWith(obj, binding.YAML) 的快捷方式。
func (c *Context) ShouldBindYAML(obj any) error {
	return c.ShouldBindWith(obj, binding.YAML)
}

// ShouldBindTOML 是 c.ShouldBindWith(obj, binding.TOML) 的快捷方式。
func (c *Context) ShouldBindTOML(obj any) error {
	return c.ShouldBindWith(obj, binding.TOML)
}

// ShouldBindPlain 是 c.ShouldBindWith(obj, binding.Plain) 的快捷方式。
func (c *Context) ShouldBindPlain(obj any) error {
	return c.ShouldBindWith(obj, binding.Plain)
}

// ShouldBindHeader 是 c.ShouldBindWith(obj, binding.Header) 的快捷方式。
func (c *Context) ShouldBindHeader(obj any) error {
	return c.ShouldBindWith(obj, binding.Header)
}

// ShouldBindUri 使用指定的绑定引擎绑定传递的结构体指针。
func (c *Context) ShouldBindUri(obj any) error {
	m := make(map[string][]string, len(c.Params))
	for _, v := range c.Params {
		m[v.Key] = []string{v.Value}
	}
	return binding.Uri.BindUri(m, obj)
}

// ShouldBindWith 使用指定的绑定引擎绑定传递的结构体指针。
// 详见 binding 包。
func (c *Context) ShouldBindWith(obj any, b binding.Binding) error {
	return b.Bind(c.Request, obj)
}

// ShouldBindBodyWith 类似于 ShouldBindWith，但它将请求体存储在上下文中，并在再次调用时重用。
//
// 注意：此方法在绑定之前读取 body。因此，如果您只需要调用一次，应使用 ShouldBindWith 以获得更好的性能。
func (c *Context) ShouldBindBodyWith(obj any, bb binding.BindingBody) (err error) {
	var body []byte
	if cb, ok := c.Get(BodyBytesKey); ok {
		if cbb, ok := cb.([]byte); ok {
			body = cbb
		}
	}
	if body == nil {
		body, err = io.ReadAll(c.Request.Body)
		if err != nil {
			return err
		}
		c.Set(BodyBytesKey, body)
	}
	return bb.BindBody(body, obj)
}

// ShouldBindBodyWithJSON 是 c.ShouldBindBodyWith(obj, binding.JSON) 的快捷方式。
func (c *Context) ShouldBindBodyWithJSON(obj any) error {
	return c.ShouldBindBodyWith(obj, binding.JSON)
}

// ShouldBindBodyWithXML 是 c.ShouldBindBodyWith(obj, binding.XML) 的快捷方式。
func (c *Context) ShouldBindBodyWithXML(obj any) error {
	return c.ShouldBindBodyWith(obj, binding.XML)
}

// ShouldBindBodyWithYAML 是 c.ShouldBindBodyWith(obj, binding.YAML) 的快捷方式。
func (c *Context) ShouldBindBodyWithYAML(obj any) error {
	return c.ShouldBindBodyWith(obj, binding.YAML)
}

// ShouldBindBodyWithTOML 是 c.ShouldBindBodyWith(obj, binding.TOML) 的快捷方式。
func (c *Context) ShouldBindBodyWithTOML(obj any) error {
	return c.ShouldBindBodyWith(obj, binding.TOML)
}

// ShouldBindBodyWithPlain 是 c.ShouldBindBodyWith(obj, binding.Plain) 的快捷方式。
func (c *Context) ShouldBindBodyWithPlain(obj any) error {
	return c.ShouldBindBodyWith(obj, binding.Plain)
}

// ClientIP 实现了一种尽力而为的算法来返回真实的客户端 IP。
// 它在内部调用 c.RemoteIP()，以检查远程 IP 是否为受信任的代理。
// 如果是，它将尝试解析 Engine.RemoteIPHeaders 中定义的头（默认为 [X-Forwarded-For, X-Real-Ip]）。
// 如果这些头在语法上无效或远程 IP 不属于受信任的代理，返回远程 IP（来自 Request.RemoteAddr）。
func (c *Context) ClientIP() string {
	// 检查我们是否在受信任的平台上运行，如果出错则继续向后运行
	if c.engine.TrustedPlatform != "" {
		// 开发人员可以定义自己的受信任平台头或使用预定义的常量
		if addr := c.requestHeader(c.engine.TrustedPlatform); addr != "" {
			return addr
		}
	}

	// 传统的 "AppEngine" 标志
	if c.engine.AppEngine {
		log.Println(`AppEngine 标志即将被弃用。请检查 issues #2723 和 #2739，并使用 'TrustedPlatform: gin.PlatformGoogleAppEngine' 代替。`)
		if addr := c.requestHeader("X-Appengine-Remote-Addr"); addr != "" {
			return addr
		}
	}

	// 它还会检查 remoteIP 是否为受信任的代理。
	// 为了进行此验证，它将查看 IP 是否包含在由 Engine.SetTrustedProxies() 定义的至少一个 CIDR 块中
	remoteIP := net.ParseIP(c.RemoteIP())
	if remoteIP == nil {
		return ""
	}
	trusted := c.engine.isTrustedProxy(remoteIP)

	if trusted && c.engine.ForwardedByClientIP && c.engine.RemoteIPHeaders != nil {
		for _, headerName := range c.engine.RemoteIPHeaders {
			ip, valid := c.engine.validateHeader(c.requestHeader(headerName))
			if valid {
				return ip
			}
		}
	}
	return remoteIP.String()
}

// RemoteIP 解析来自 Request.RemoteAddr 的 IP，规范化并返回 IP（不带端口）。
func (c *Context) RemoteIP() string {
	ip, _, err := net.SplitHostPort(strings.TrimSpace(c.Request.RemoteAddr))
	if err != nil {
		return ""
	}
	return ip
}

// ContentType 返回请求的 Content-Type 头。
func (c *Context) ContentType() string {
	return filterFlags(c.requestHeader("Content-Type"))
}

// IsWebsocket 如果请求头表示客户端正在发起 websocket 握手，则返回 true。
func (c *Context) IsWebsocket() bool {
	if strings.Contains(strings.ToLower(c.requestHeader("Connection")), "upgrade") &&
		strings.EqualFold(c.requestHeader("Upgrade"), "websocket") {
		return true
	}
	return false
}

func (c *Context) requestHeader(key string) string {
	return c.Request.Header.Get(key)
}

/************************************/
/************** 响应渲染 *************/
/************************************/

// bodyAllowedForStatus 是 http.bodyAllowedForStatus 非导出函数的副本。
func bodyAllowedForStatus(status int) bool {
	switch {
	case status >= 100 && status <= 199:
		return false
	case status == http.StatusNoContent:
		return false
	case status == http.StatusNotModified:
		return false
	}
	return true
}

// Status 设置 HTTP 响应代码。
func (c *Context) Status(code int) {
	c.Writer.WriteHeader(code)
}

// Header 是 c.Writer.Header().Set(key, value) 的智能快捷方式。
// 它在响应中写入一个头信息。
// 如果 value == ""，此方法将删除头信息 `c.Writer.Header().Del(key)`
func (c *Context) Header(key, value string) {
	if value == "" {
		c.Writer.Header().Del(key)
		return
	}
	c.Writer.Header().Set(key, value)
}

// GetHeader 从请求头中返回值。
func (c *Context) GetHeader(key string) string {
	return c.requestHeader(key)
}

// GetRawData 返回流数据。
func (c *Context) GetRawData() ([]byte, error) {
	if c.Request.Body == nil {
		return nil, errors.New("无法读取空的请求体")
	}
	return io.ReadAll(c.Request.Body)
}

// SetSameSite 设置 cookie 的 SameSite 属性
func (c *Context) SetSameSite(samesite http.SameSite) {
	c.sameSite = samesite
}

// SetCookie 向 ResponseWriter 的头中添加一个 Set-Cookie 头。
// 提供的 cookie 必须有一个有效的名称。无效的 cookie 可能会被静默丢弃。
func (c *Context) SetCookie(name, value string, maxAge int, path, domain string, secure, httpOnly bool) {
	if path == "" {
		path = "/"
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    url.QueryEscape(value),
		MaxAge:   maxAge,
		Path:     path,
		Domain:   domain,
		SameSite: c.sameSite,
		Secure:   secure,
		HttpOnly: httpOnly,
	})
}

// Cookie 返回请求中提供的指定名称的 cookie，如果未找到则返回 ErrNoCookie。
// 返回的 cookie 的值是解码后的。
// 如果有多个 cookie 匹配给定的名称，只会返回一个。
func (c *Context) Cookie(name string) (string, error) {
	cookie, err := c.Request.Cookie(name)
	if err != nil {
		return "", err
	}
	val, _ := url.QueryUnescape(cookie.Value)
	return val, nil
}

// Render 写入响应头并调用 render.Render 来渲染数据。
func (c *Context) Render(code int, r render.Render) {
	c.Status(code)

	if !bodyAllowedForStatus(code) {
		r.WriteContentType(c.Writer)
		c.Writer.WriteHeaderNow()
		return
	}

	if err := r.Render(c.Writer); err != nil {
		// 将错误推送到 c.Errors
		_ = c.Error(err)
		c.Abort()
	}
}

// HTML 渲染由文件名指定的 HTTP 模板。
// 它还会更新 HTTP 状态码并将 Content-Type 设置为 "text/html"。
// 参见 http://golang.org/doc/articles/wiki/
func (c *Context) HTML(code int, name string, obj any) {
	instance := c.engine.HTMLRender.Instance(name, obj)
	c.Render(code, instance)
}

// IndentedJSON 将给定的结构序列化为格式化的 JSON（带缩进和换行符）到响应体中。
// 它还将 Content-Type 设置为 "application/json"。
// 警告：我们建议仅在开发环境中使用此方法，因为打印格式化的 JSON 会消耗更多的 CPU 和带宽。
// 请使用 Context.JSON() 代替。
func (c *Context) IndentedJSON(code int, obj any) {
	c.Render(code, render.IndentedJSON{Data: obj})
}

// SecureJSON 将给定的结构序列化为安全的 JSON 到响应体中。
// 默认情况下，如果给定的结构是数组值，它会在响应体前添加 "while(1),"。
// 它还将 Content-Type 设置为 "application/json"。
func (c *Context) SecureJSON(code int, obj any) {
	c.Render(code, render.SecureJSON{Prefix: c.engine.secureJSONPrefix, Data: obj})
}

// JSONP 将给定的结构序列化为 JSON 到响应体中。
// 它在响应体中添加填充，以便从与客户端不同域的服务器请求数据。
// 它还将 Content-Type 设置为 "application/javascript"。
func (c *Context) JSONP(code int, obj any) {
	callback := c.DefaultQuery("callback", "")
	if callback == "" {
		c.Render(code, render.JSON{Data: obj})
		return
	}
	c.Render(code, render.JsonpJSON{Callback: callback, Data: obj})
}

// JSON 将给定的结构序列化为 JSON 到响应体中。
// 它还将 Content-Type 设置为 "application/json"。
func (c *Context) JSON(code int, obj any) {
	c.Render(code, render.JSON{Data: obj})
}

// AsciiJSON 将给定的结构序列化为 JSON 到响应体中，并将 Unicode 转换为 ASCII 字符串。
// 它还将 Content-Type 设置为 "application/json"。
func (c *Context) AsciiJSON(code int, obj any) {
	c.Render(code, render.AsciiJSON{Data: obj})
}

// PureJSON 将给定的结构序列化为 JSON 到响应体中。
// 与 JSON 不同，PureJSON 不会将特殊的 HTML 字符替换为它们的 Unicode 实体。
func (c *Context) PureJSON(code int, obj any) {
	c.Render(code, render.PureJSON{Data: obj})
}

// XML 将给定的结构序列化为 XML 到响应体中。
// 它还将 Content-Type 设置为 "application/xml"。
func (c *Context) XML(code int, obj any) {
	c.Render(code, render.XML{Data: obj})
}

// YAML 将给定的结构序列化为 YAML 到响应体中。
func (c *Context) YAML(code int, obj any) {
	c.Render(code, render.YAML{Data: obj})
}

// TOML 将给定的结构序列化为 TOML 到响应体中。
func (c *Context) TOML(code int, obj any) {
	c.Render(code, render.TOML{Data: obj})
}

// ProtoBuf 将给定的结构序列化为 ProtoBuf 到响应体中。
func (c *Context) ProtoBuf(code int, obj any) {
	c.Render(code, render.ProtoBuf{Data: obj})
}

// String 将给定的字符串写入响应体中。
func (c *Context) String(code int, format string, values ...any) {
	c.Render(code, render.String{Format: format, Data: values})
}

// Redirect 返回一个 HTTP 重定向到指定位置。
func (c *Context) Redirect(code int, location string) {
	c.Render(-1, render.Redirect{
		Code:     code,
		Location: location,
		Request:  c.Request,
	})
}

// Data 将一些数据写入响应体流并更新 HTTP 状态码。
func (c *Context) Data(code int, contentType string, data []byte) {
	c.Render(code, render.Data{
		ContentType: contentType,
		Data:        data,
	})
}

// DataFromReader 将指定的 reader 写入响应体流并更新 HTTP 状态码。
func (c *Context) DataFromReader(code int, contentLength int64, contentType string, reader io.Reader, extraHeaders map[string]string) {
	c.Render(code, render.Reader{
		Headers:       extraHeaders,
		ContentType:   contentType,
		ContentLength: contentLength,
		Reader:        reader,
	})
}

// File 以高效的方式将指定的文件写入响应体流。
func (c *Context) File(filepath string) {
	http.ServeFile(c.Writer, c.Request, filepath)
}

// FileFromFS 以高效的方式将 http.FileSystem 中的指定文件写入响应体流。
func (c *Context) FileFromFS(filepath string, fs http.FileSystem) {
	defer func(old string) {
		c.Request.URL.Path = old
	}(c.Request.URL.Path)

	c.Request.URL.Path = filepath

	http.FileServer(fs).ServeHTTP(c.Writer, c.Request)
}

var quoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"")

func escapeQuotes(s string) string {
	return quoteEscaper.Replace(s)
}

// FileAttachment 以高效的方式将指定的文件写入响应体流。
// 在客户端，文件通常会以给定的文件名下载。
func (c *Context) FileAttachment(filepath, filename string) {
	if isASCII(filename) {
		c.Writer.Header().Set("Content-Disposition", `attachment; filename="`+escapeQuotes(filename)+`"`)
	} else {
		c.Writer.Header().Set("Content-Disposition", `attachment; filename*=UTF-8''`+url.QueryEscape(filename))
	}
	http.ServeFile(c.Writer, c.Request, filepath)
}

// SSEvent 将一个服务器发送事件（Server-Sent Event）写入响应体流。
func (c *Context) SSEvent(name string, message any) {
	c.Render(-1, sse.Event{
		Event: name,
		Data:  message,
	})
}

// Stream 发送一个流响应，并返回一个布尔值，指示“客户端是否在流中途断开连接”。
func (c *Context) Stream(step func(w io.Writer) bool) bool {
	w := c.Writer
	clientGone := w.CloseNotify()
	for {
		select {
		case <-clientGone:
			return true
		default:
			keepOpen := step(w)
			w.Flush()
			if !keepOpen {
				return false
			}
		}
	}
}

/************************************/
/*********** 内容协商 ****************/
/************************************/

// Negotiate 包含所有协商数据。
type Negotiate struct {
	Offered  []string
	HTMLName string
	HTMLData any
	JSONData any
	XMLData  any
	YAMLData any
	Data     any
	TOMLData any
}

// Negotiate 根据可接受的 Accept 格式调用不同的 Render 方法。
func (c *Context) Negotiate(code int, config Negotiate) {
	switch c.NegotiateFormat(config.Offered...) {
	case binding.MIMEJSON:
		data := chooseData(config.JSONData, config.Data)
		c.JSON(code, data)

	case binding.MIMEHTML:
		data := chooseData(config.HTMLData, config.Data)
		c.HTML(code, config.HTMLName, data)

	case binding.MIMEXML:
		data := chooseData(config.XMLData, config.Data)
		c.XML(code, data)

	case binding.MIMEYAML, binding.MIMEYAML2:
		data := chooseData(config.YAMLData, config.Data)
		c.YAML(code, data)

	case binding.MIMETOML:
		data := chooseData(config.TOMLData, config.Data)
		c.TOML(code, data)

	default:
		c.AbortWithError(http.StatusNotAcceptable, errors.New("the accepted formats are not offered by the server")) //nolint: errcheck
	}
}

// NegotiateFormat 返回一个可接受的 Accept 格式。
func (c *Context) NegotiateFormat(offered ...string) string {
	assert1(len(offered) > 0, "you must provide at least one offer")

	if c.Accepted == nil {
		c.Accepted = parseAccept(c.requestHeader("Accept"))
	}
	if len(c.Accepted) == 0 {
		return offered[0]
	}
	for _, accepted := range c.Accepted {
		for _, offer := range offered {
			// 根据 RFC 2616 和 RFC 2396，非 ASCII 字符不允许出现在头部，
			// 因此我们可以直接遍历字符串而无需将其转换为 []rune
			i := 0
			for ; i < len(accepted) && i < len(offer); i++ {
				if accepted[i] == '*' || offer[i] == '*' {
					return offer
				}
				if accepted[i] != offer[i] {
					break
				}
			}
			if i == len(accepted) {
				return offer
			}
		}
	}
	return ""
}

// SetAccepted 设置 Accept 头部数据。
func (c *Context) SetAccepted(formats ...string) {
	c.Accepted = formats
}

/************************************/
/***** GOLANG.ORG/X/NET/CONTEXT *****/
/************************************/

// hasRequestContext 返回 c.Request 是否具有上下文和备用处理。
func (c *Context) hasRequestContext() bool {
	hasFallback := c.engine != nil && c.engine.ContextWithFallback
	hasRequestContext := c.Request != nil && c.Request.Context() != nil
	return hasFallback && hasRequestContext
}

// Deadline 在 c.Request 没有上下文时返回没有截止时间 (ok==false)。
func (c *Context) Deadline() (deadline time.Time, ok bool) {
	if !c.hasRequestContext() {
		return
	}
	return c.Request.Context().Deadline()
}

// Done 在 c.Request 没有上下文时返回 nil（一个将永远等待的通道）。
func (c *Context) Done() <-chan struct{} {
	if !c.hasRequestContext() {
		return nil
	}
	return c.Request.Context().Done()
}

// Err 在 c.Request 没有上下文时返回 nil。
func (c *Context) Err() error {
	if !c.hasRequestContext() {
		return nil
	}
	return c.Request.Context().Err()
}

// Value 返回与此上下文关联的 key 的值，如果没有与 key 关联的值，则返回 nil。
// 多次调用 Value 使用相同的 key 会返回相同的结果。
func (c *Context) Value(key any) any {
	if key == ContextRequestKey {
		return c.Request
	}
	if key == ContextKey {
		return c
	}
	if keyAsString, ok := key.(string); ok {
		if val, exists := c.Get(keyAsString); exists {
			return val
		}
	}
	if !c.hasRequestContext() {
		return nil
	}
	return c.Request.Context().Value(key)
}
