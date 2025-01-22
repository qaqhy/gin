// Copyright 2014 Manu Martinez-Almeida. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package gin

import (
	"flag"
	"io"
	"os"
	"sync/atomic"

	"github.com/gin-gonic/gin/binding"
)

// EnvGinMode indicates environment name for gin mode.
const EnvGinMode = "GIN_MODE"

const (
	// DebugMode indicates gin mode is debug.
	DebugMode = "debug"
	// ReleaseMode indicates gin mode is release.
	ReleaseMode = "release"
	// TestMode indicates gin mode is test.
	TestMode = "test"
)

const (
	debugCode = iota
	releaseCode
	testCode
)

// DefaultWriter is the default io.Writer used by Gin for debug output and
// middleware output like Logger() or Recovery().
// Note that both Logger and Recovery provides custom ways to configure their
// output io.Writer.
// To support coloring in Windows use:
//
//	import "github.com/mattn/go-colorable"
//	gin.DefaultWriter = colorable.NewColorableStdout()
var DefaultWriter io.Writer = os.Stdout

// DefaultErrorWriter is the default io.Writer used by Gin to debug errors
var DefaultErrorWriter io.Writer = os.Stderr

var ginMode int32 = debugCode
var modeName atomic.Value

func init() {
	mode := os.Getenv(EnvGinMode)
	SetMode(mode)
}

// SetMode sets gin mode according to input string.
func SetMode(value string) {
	if value == "" {
		if flag.Lookup("test.v") != nil {
			value = TestMode
		} else {
			value = DebugMode
		}
	}

	switch value {
	case DebugMode, "":
		atomic.StoreInt32(&ginMode, debugCode)
	case ReleaseMode:
		atomic.StoreInt32(&ginMode, releaseCode)
	case TestMode:
		atomic.StoreInt32(&ginMode, testCode)
	default:
		panic("gin mode unknown: " + value + " (available mode: debug release test)")
	}
	modeName.Store(value)
}

// DisableBindValidation 关闭默认的验证器。
func DisableBindValidation() {
	binding.Validator = nil
}

// EnableJsonDecoderUseNumber 设置 binding.EnableDecoderUseNumber 为 true，
// 从而在 JSON Decoder 实例上调用 UseNumber 方法。
func EnableJsonDecoderUseNumber() {
	binding.EnableDecoderUseNumber = true
}

// EnableJsonDecoderDisallowUnknownFields 设置 binding.EnableDecoderDisallowUnknownFields 为 true，
// 从而在 JSON Decoder 实例上调用 DisallowUnknownFields 方法。
func EnableJsonDecoderDisallowUnknownFields() {
	binding.EnableDecoderDisallowUnknownFields = true
}

// Mode 返回当前的 gin 模式。
func Mode() string {
	return modeName.Load().(string)
}
