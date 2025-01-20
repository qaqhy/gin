// Copyright 2013 Julien Schmidt. All rights reserved.
// Based on the path package, Copyright 2009 The Go Authors.
// Use of this source code is governed by a BSD-style license that can be found
// at https://github.com/julienschmidt/httprouter/blob/master/LICENSE.

package gin

// cleanPath 是 URL 版本的 path.Clean，它返回 p 的规范化 URL 路径，消除 . 和 .. 元素。
//
// 以下规则会迭代应用，直到无法进一步处理：
//  1. 用单个斜杠替换多个斜杠。
//  2. 消除每个 . 路径名元素（当前目录）。
//  3. 消除每个内部的 .. 路径名元素（父目录）及其前面的非 .. 元素。
//  4. 消除以 .. 开头的根路径元素：
//     也就是说，在路径的开头将 "/.." 替换为 "/"。
//
// 如果处理的结果是一个空字符串，将返回 "/"。
func cleanPath(p string) string {
	const stackBufSize = 128
	// 将空字符串转换为 "/"
	if p == "" {
		return "/"
	}

	// 在堆栈上创建合理大小的缓冲区，以避免在常见情况下的分配。
	// 如果需要更大的缓冲区，则会动态分配。
	buf := make([]byte, 0, stackBufSize)

	n := len(p)

	// 不变量：
	//      从路径读取；r 是下一个要处理的字节的索引。
	//      向缓冲区写入；w 是下一个要写入的字节的索引。

	// 路径必须以 '/' 开头
	r := 1
	w := 1

	if p[0] != '/' {
		r = 0

		if n+1 > stackBufSize {
			buf = make([]byte, n+1)
		} else {
			buf = buf[:n+1]
		}
		buf[0] = '/'
	}

	trailing := n > 1 && p[n-1] == '/'

	// 没有 'lazybuf' 的情况下有点笨拙，但循环完全内联（bufApp 调用）。
	// 循环没有昂贵的函数调用（除了一次 make 调用）。
	for r < n {
		switch {
		case p[r] == '/':
			// 空路径元素，尾随斜杠在末尾添加
			r++

		case p[r] == '.' && r+1 == n:
			trailing = true
			r++

		case p[r] == '.' && p[r+1] == '/':
			// . 元素
			r += 2

		case p[r] == '.' && p[r+1] == '.' && (r+2 == n || p[r+2] == '/'):
			// .. 元素：移除到上一个 /
			r += 3

			if w > 1 {
				// 可以回溯
				w--

				if len(buf) == 0 {
					for w > 1 && p[w] != '/' {
						w--
					}
				} else {
					for w > 1 && buf[w] != '/' {
						w--
					}
				}
			}

		default:
			// 实际路径元素。
			// 如果需要，添加斜杠
			if w > 1 {
				bufApp(&buf, p, w, '/')
				w++
			}

			// 复制元素
			for r < n && p[r] != '/' {
				bufApp(&buf, p, w, p[r])
				w++
				r++
			}
		}
	}

	// 重新添加尾随斜杠
	if trailing && w > 1 {
		bufApp(&buf, p, w, '/')
		w++
	}

	// 如果原始字符串没有被修改（或只是在末尾缩短），
	// 返回原始字符串的相应子字符串。
	// 否则，从缓冲区返回一个新字符串。
	if len(buf) == 0 {
		return p[:w]
	}
	return string(buf[:w])
}

// 内部辅助函数，在必要时懒惰地创建缓冲区。
// 对此函数的调用会被内联。
func bufApp(buf *[]byte, s string, w int, c byte) {
	b := *buf
	if len(b) == 0 {
		// 到目前为止没有修改原始字符串。
		// 如果下一个字符与原始字符串中的字符相同，我们还不需要分配缓冲区。
		if s[w] == c {
			return
		}

		// 否则，使用堆栈缓冲区（如果它足够大），或者在堆上分配一个新缓冲区，并复制所有以前的字符。
		length := len(s)
		if length > cap(b) {
			*buf = make([]byte, length)
		} else {
			*buf = (*buf)[:length]
		}
		b = *buf

		copy(b, s[:w])
	}
	b[w] = c
}
