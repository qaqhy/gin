// Copyright 2013 Julien Schmidt. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// at https://github.com/julienschmidt/httprouter/blob/master/LICENSE

package gin

import (
	"bytes"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin/internal/bytesconv"
)

var (
	strColon = []byte(":")
	strStar  = []byte("*")
	strSlash = []byte("/")
)

// Param is a single URL parameter, consisting of a key and a value.
type Param struct {
	Key   string
	Value string
}

// Params is a Param-slice, as returned by the router.
// The slice is ordered, the first URL parameter is also the first slice value.
// It is therefore safe to read values by the index.
type Params []Param

// Get returns the value of the first Param which key matches the given name and a boolean true.
// If no matching Param is found, an empty string is returned and a boolean false .
func (ps Params) Get(name string) (string, bool) {
	for _, entry := range ps {
		if entry.Key == name {
			return entry.Value, true
		}
	}
	return "", false
}

// ByName returns the value of the first Param which key matches the given name.
// If no matching Param is found, an empty string is returned.
func (ps Params) ByName(name string) (va string) {
	va, _ = ps.Get(name)
	return
}

type methodTree struct {
	method string
	root   *node
}

type methodTrees []methodTree

// get 返回给定HTTP方法的节点。
func (trees methodTrees) get(method string) *node {
	for _, tree := range trees {
		if tree.method == method {
			return tree.root
		}
	}
	return nil
}

// longestCommonPrefix 返回a和b的最长公共前缀长度。
func longestCommonPrefix(a, b string) int {
	i := 0
	max_ := min(len(a), len(b))
	for i < max_ && a[i] == b[i] {
		i++
	}
	return i
}

// addChild 将添加一个子节点，并将通配符子节点保持在最后
func (n *node) addChild(child *node) {
	if n.wildChild && len(n.children) > 0 {
		wildcardChild := n.children[len(n.children)-1]
		n.children = append(n.children[:len(n.children)-1], child, wildcardChild)
	} else {
		n.children = append(n.children, child)
	}
}

func countParams(path string) uint16 {
	var n uint16
	s := bytesconv.StringToBytes(path)
	n += uint16(bytes.Count(s, strColon))
	n += uint16(bytes.Count(s, strStar))
	return n
}

func countSections(path string) uint16 {
	s := bytesconv.StringToBytes(path)
	return uint16(bytes.Count(s, strSlash))
}

type nodeType uint8

const (
	static nodeType = iota
	root
	param
	catchAll
)

type node struct {
	path      string // 节点路径
	indices   string
	wildChild bool // 是否包含通配符子节点
	// 节点类型，包括static, root, param, catchAll
	// 	static: 静态节点
	// 	root: 树的根节点
	// 	catchAll: 有*匹配的节点
	// 	param: 参数节点
	nType    nodeType
	priority uint32        // 优先级，子节点注册的handler数量
	children []*node       // 子节点数组，最多包含一个 :param 风格的节点，且位于数组的末尾
	handlers HandlersChain // 路由对应的handler函数
	fullPath string        // 完整节点路径，比如上面的
}

// incrementChildPrio 增加给定子节点的优先级，并在必要时重新排序
func (n *node) incrementChildPrio(pos int) int {
	cs := n.children
	cs[pos].priority++
	prio := cs[pos].priority

	// 调整位置（移动到前面）
	newPos := pos
	for ; newPos > 0 && cs[newPos-1].priority < prio; newPos-- {
		// 交换节点位置
		cs[newPos-1], cs[newPos] = cs[newPos], cs[newPos-1]
	}

	// 构建新的索引字符字符串
	if newPos != pos {
		n.indices = n.indices[:newPos] + // 未改变的前缀，可能为空
			n.indices[pos:pos+1] + // 我们移动的索引字符
			n.indices[newPos:pos] + n.indices[pos+1:] // 剩余部分不包括位置'pos'的字符
	}

	return newPos
}

// addRoute 添加一个具有给定处理器的节点到路径(默克尔压缩前缀树)。
// 不支持并发安全！
func (n *node) addRoute(path string, handlers HandlersChain) {
	fullPath := path // 完整路径
	n.priority++     // 每有一个新路由经过此节点，priority 都要加 1

	// 加入当前节点为 root 且未注册过子节点，则直接插入并返回
	if len(n.path) == 0 && len(n.children) == 0 {
		n.insertChild(path, fullPath, handlers)
		n.nType = root // 设置节点类型为根节点
		return
	}

	parentFullPathIndex := 0 // 父节点完整路径的索引

walk: // 外层 for 循环断点
	for {
		// 找到最长的公共前缀。
		// 这也意味着公共前缀不包含 ':' 或 '*'，因为现有的键不能包含这些字符。
		i := longestCommonPrefix(path, n.path)

		// 分割边缘
		if i < len(n.path) { // 如果公共前缀的长度小于节点路径的长度，则创建一个子节点
			child := node{
				path:      n.path[i:],
				wildChild: n.wildChild,
				nType:     static,
				indices:   n.indices,
				children:  n.children,
				handlers:  n.handlers,
				priority:  n.priority - 1,
				fullPath:  n.fullPath,
			}

			n.children = []*node{&child}
			// []byte用于正确的Unicode字符转换，见#65
			n.indices = bytesconv.BytesToString([]byte{n.path[i]})
			n.path = path[:i]
			n.handlers = nil
			n.wildChild = false
			n.fullPath = fullPath[:parentFullPathIndex+i]
		}

		// 使新节点成为此节点的子节点
		if i < len(path) {
			path = path[i:]
			c := path[0]

			// 参数后的 '/'
			if n.nType == param && c == '/' && len(n.children) == 1 {
				parentFullPathIndex += len(n.path)
				n = n.children[0]
				n.priority++
				continue walk
			}

			// 检查是否存在具有下一个路径字节的子节点
			for i, max_ := 0, len(n.indices); i < max_; i++ {
				if c == n.indices[i] {
					parentFullPathIndex += len(n.path)
					i = n.incrementChildPrio(i)
					n = n.children[i]
					continue walk
				}
			}

			// 否则插入它
			if c != ':' && c != '*' && n.nType != catchAll {
				// []byte用于正确的Unicode字符转换，见#65
				n.indices += bytesconv.BytesToString([]byte{c})
				child := &node{
					fullPath: fullPath,
				}
				n.addChild(child)
				n.incrementChildPrio(len(n.indices) - 1)
				n = child
			} else if n.wildChild {
				// 插入一个通配符节点，需要检查它是否与现有通配符冲突
				n = n.children[len(n.children)-1]
				n.priority++

				// 检查通配符是否匹配
				if len(path) >= len(n.path) && n.path == path[:len(n.path)] &&
					// 不可能为 catchAll 添加子节点
					n.nType != catchAll &&
					// 检查更长的通配符，例如 :name 和 :names
					(len(n.path) >= len(path) || path[len(n.path)] == '/') {
					continue walk
				}

				// 通配符冲突
				pathSeg := path
				if n.nType != catchAll {
					pathSeg = strings.SplitN(pathSeg, "/", 2)[0]
				}
				prefix := fullPath[:strings.Index(fullPath, pathSeg)] + n.path
				panic("'" + pathSeg +
					"' in new path '" + fullPath +
					"' conflicts with existing wildcard '" + n.path +
					"' in existing prefix '" + prefix +
					"'")
			}

			n.insertChild(path, fullPath, handlers)
			return
		}

		// 否则添加处理器到当前节点
		if n.handlers != nil {
			panic("handlers are already registered for path '" + fullPath + "'")
		}
		n.handlers = handlers
		n.fullPath = fullPath
		return
	}
}

// findWildcard 查找一个通配符片段并检查名称中是否有无效字符。
// 如果没有找到通配符，则返回 -1 作为索引。
//  1. path:[/a/b/c]
//     => wildcard:[], i:[-1], valid:[false]
//  2. path:[/a/b/:c]
//     => wildcard:[:c], i:[5], valid:[true]
//  3. path:[/a/:b/:c]
//     => wildcard:[:b], i:[3], valid:[true]
//  4. path:[/a/b*c]
//     => wildcard:[*c], i:[4], valid:[true]
//  5. path:[/a/:*/:c] 或 [/a/:*/c] 或 [/a/:*]
//     => wildcard:[:*], i:[3], valid:[false]
//  6. path:[/a/*/:c] 或 [/a/*/c] 或 [/a/*]
//     => wildcard:[*], i:[3], valid:[true]
//  7. path:[/a/b\\c] 或 [/a/\\c] 或 [\\a]
//     => panic:[/a/b\c] 或 [/a/\c] 或 [\a]
func findWildcard(path string) (wildcard string, i int, valid bool) {
	// 查找开始位置
	escapeColon := false
	for start, c := range []byte(path) {
		if escapeColon {
			escapeColon = false
			if c == ':' {
				continue
			}
			panic("invalid escape string in path '" + path + "'")
		}
		if c == '\\' {
			escapeColon = true
			continue
		}
		// 通配符以 ':'（参数）或 '*'（全捕获）开始
		if c != ':' && c != '*' {
			continue
		}

		// 查找结束位置并检查是否有无效字符
		valid = true
		for end, c := range []byte(path[start+1:]) {
			switch c {
			case '/':
				return path[start : start+1+end], start, valid
			case ':', '*':
				valid = false
			}
		}
		return path[start:], start, valid
	}
	return "", -1, false
}

// insertChild 向本树中插入子节点
//
//	path: 待插入节点的路径 eg: /a/b/:c/:d
//	fullPath: 完整路径 eg: /a/b/:c/:d
//	handlers: 处理函数
//	处理后的链路: node{path:/a/b/ wildChild:true nType:1 priority:1 fullPath: children:[]*node{
//		{path::c wildChild:false nType:2 priority:1 fullPath:/a/b/:c/:d children:[]*node{
//			{path:/ wildChild:true nType:0 priority:1 fullPath:/a/b/:c/:d children:[]*node{
//				{path::d wildChild:false nType:2 priority:1 fullPath:/a/b/:c/:d children:nil handlers:[func1 func2]}
//			}
//		}
//	}
func (n *node) insertChild(path string, fullPath string, handlers HandlersChain) {
	for {
		// 查找前缀直到第一个通配符
		// step1 path:[/a/b/:c/:d] => wildcard:[:c], i:[5], valid:[true]
		// step2 path:[/:d] => wildcard:[:d], i:[1], valid:[true]
		wildcard, i, valid := findWildcard(path)
		if i < 0 { // 没有找到通配符则退出循环
			break
		}

		// 通配符名称只能包含一个 ':' 或 '*' 字符，否则抛出此异常
		if !valid {
			panic("only one wildcard per path segment is allowed, has: '" +
				wildcard + "' in path '" + fullPath + "'")
		}

		// 检查通配符是否有名称，若仅有一个 ':' 或 '*' 字符，则抛出此异常，必须包含通配符字符的名称
		if len(wildcard) < 2 {
			panic("wildcards must be named with a non-empty name in path '" + fullPath + "'")
		}

		if wildcard[0] == ':' { // 若是参数通配符则插入参数节点，path:[/a/b/:c/:d]
			if i > 0 { // step1:[i:5] step2:[i:1]
				// 在当前通配符之前插入前缀
				n.path = path[:i] // step1:[/a/b/] step2:[/]
				path = path[i:]   // step1:[:c/:d] step2:[:d]
			}

			child := &node{
				nType:    param,
				path:     wildcard, // step1:[:c] step2:[:d]
				fullPath: fullPath, // /a/b/:c/:d
			}
			n.addChild(child)
			n.wildChild = true
			n = child
			n.priority++

			// 如果路径没有以通配符结束，则会有另一个以 '/' 开头的子路径
			if len(wildcard) < len(path) {
				path = path[len(wildcard):] // step1:[/:d]

				child := &node{
					priority: 1,
					fullPath: fullPath, // /a/b/:c/:d
				}
				n.addChild(child)
				n = child
				continue
			}

			// 否则我们已经完成。在新的叶子节点插入处理函数
			n.handlers = handlers
			return
		}

		// 全捕获
		if i+len(wildcard) != len(path) { // *param 必须在路径的最后
			panic("catch-all routes are only allowed at the end of the path in path '" + fullPath + "'")
		}

		// 如果节点上还有其他子节点，说明已经有其他 api 注册，则不能使用 *param 通配符
		// eg: /a/b/*cmm注册后/a/*xx路径不允许注册
		if len(n.path) > 0 && n.path[len(n.path)-1] == '/' {
			pathSeg := ""
			if len(n.children) != 0 {
				pathSeg = strings.SplitN(n.children[0].path, "/", 2)[0]
			}
			panic("catch-all wildcard '" + path +
				"' in new path '" + fullPath +
				"' conflicts with existing path segment '" + pathSeg +
				"' in existing prefix '" + n.path + pathSeg +
				"'")
		}

		// 目前固定宽度为 1 的 '/'
		i--
		if i < 0 || path[i] != '/' { // /a*b的路由报错
			panic("no / before catch-all in path '" + fullPath + "'")
		}

		n.path = path[:i]

		// 第一个节点：具有空路径的全捕获节点
		child := &node{
			wildChild: true,
			nType:     catchAll,
			fullPath:  fullPath,
		}

		n.addChild(child)
		n.indices = string('/')
		n = child
		n.priority++

		// 第二个节点：持有变量的节点
		child = &node{
			path:     path[i:],
			nType:    catchAll,
			handlers: handlers,
			priority: 1,
			fullPath: fullPath,
		}
		n.children = []*node{child}

		return
	}

	// 如果没有找到通配符，直接插入路径和处理函数
	n.path = path
	n.handlers = handlers
	n.fullPath = fullPath
}

// nodeValue 保存 (*Node).getValue 方法的返回值
type nodeValue struct {
	handlers HandlersChain
	params   *Params
	tsr      bool
	fullPath string
}

type skippedNode struct {
	path        string
	node        *node
	paramsCount int16
}

// getValue 返回注册了给定路径（键）的处理程序。通配符的值被保存到一个映射中。
// 如果找不到处理程序，则会建议进行 TSR（尾部斜杠重定向），如果存在一个
// 处理程序在给定路径的尾部斜杠多或少一个的情况下。
func (n *node) getValue(path string, params *Params, skippedNodes *[]skippedNode, unescape bool) (value nodeValue) {
	var globalParamsCount int16

walk: // 遍历树的外部循环
	for {
		prefix := n.path
		if len(path) > len(prefix) {
			if path[:len(prefix)] == prefix {
				path = path[len(prefix):]

				// 先尝试所有非通配符的子节点，通过匹配索引
				idxc := path[0]
				for i, c := range []byte(n.indices) {
					if c == idxc {
						//  strings.HasPrefix(n.children[len(n.children)-1].path, ":") == n.wildChild
						if n.wildChild {
							index := len(*skippedNodes)
							*skippedNodes = (*skippedNodes)[:index+1]
							(*skippedNodes)[index] = skippedNode{
								path: prefix + path,
								node: &node{
									path:      n.path,
									wildChild: n.wildChild,
									nType:     n.nType,
									priority:  n.priority,
									children:  n.children,
									handlers:  n.handlers,
									fullPath:  n.fullPath,
								},
								paramsCount: globalParamsCount,
							}
						}

						n = n.children[i]
						continue walk
					}
				}

				if !n.wildChild {
					// 如果在循环结束时的路径不等于 '/' 并且当前节点没有子节点
					// 则当前节点需要回滚到最后一个有效的 skippedNode
					if path != "/" {
						for length := len(*skippedNodes); length > 0; length-- {
							skippedNode := (*skippedNodes)[length-1]
							*skippedNodes = (*skippedNodes)[:length-1]
							if strings.HasSuffix(skippedNode.path, path) {
								path = skippedNode.path
								n = skippedNode.node
								if value.params != nil {
									*value.params = (*value.params)[:skippedNode.paramsCount]
								}
								globalParamsCount = skippedNode.paramsCount
								continue walk
							}
						}
					}

					// 没有找到。
					// 我们可以建议重定向到相同的 URL，但没有尾部斜杠，
					// 如果该路径存在叶子节点。
					value.tsr = path == "/" && n.handlers != nil
					return value
				}

				// 处理通配符子节点，该节点总是在数组的末尾
				n = n.children[len(n.children)-1]
				globalParamsCount++

				switch n.nType {
				case param:
					// 修复截断参数
					// tree_test.go 第204行

					// 找到参数的结尾（'/' 或路径结尾）
					end := 0
					for end < len(path) && path[end] != '/' {
						end++
					}

					// 保存参数值
					if params != nil {
						// 如果需要预分配容量
						if cap(*params) < int(globalParamsCount) {
							newParams := make(Params, len(*params), globalParamsCount)
							copy(newParams, *params)
							*params = newParams
						}

						if value.params == nil {
							value.params = params
						}
						// 在预分配的容量内扩展切片
						i := len(*value.params)
						*value.params = (*value.params)[:i+1]
						val := path[:end]
						if unescape {
							if v, err := url.QueryUnescape(val); err == nil {
								val = v
							}
						}
						(*value.params)[i] = Param{
							Key:   n.path[1:],
							Value: val,
						}
					}

					// 我们需要深入！
					if end < len(path) {
						if len(n.children) > 0 {
							path = path[end:]
							n = n.children[0]
							continue walk
						}

						// ... 但是我们不能
						value.tsr = len(path) == end+1
						return value
					}

					if value.handlers = n.handlers; value.handlers != nil {
						value.fullPath = n.fullPath
						return value
					}
					if len(n.children) == 1 {
						// 没有找到处理程序。检查是否存在该路径 + 尾部斜杠的处理程序
						// 以便进行 TSR 建议
						n = n.children[0]
						value.tsr = (n.path == "/" && n.handlers != nil) || (n.path == "" && n.indices == "/")
					}
					return value

				case catchAll:
					// 保存参数值
					if params != nil {
						// 如果需要预分配容量
						if cap(*params) < int(globalParamsCount) {
							newParams := make(Params, len(*params), globalParamsCount)
							copy(newParams, *params)
							*params = newParams
						}

						if value.params == nil {
							value.params = params
						}
						// 在预分配的容量内扩展切片
						i := len(*value.params)
						*value.params = (*value.params)[:i+1]
						val := path
						if unescape {
							if v, err := url.QueryUnescape(path); err == nil {
								val = v
							}
						}
						(*value.params)[i] = Param{
							Key:   n.path[2:],
							Value: val,
						}
					}

					value.handlers = n.handlers
					value.fullPath = n.fullPath
					return value

				default:
					panic("invalid node type")
				}
			}
		}

		if path == prefix {
			// 如果当前路径不等于 '/' 并且节点没有注册处理函数，并且最近匹配的节点有一个子节点
			// 当前节点需要回滚到最后一个有效的 skippedNode
			if n.handlers == nil && path != "/" {
				for length := len(*skippedNodes); length > 0; length-- {
					skippedNode := (*skippedNodes)[length-1]
					*skippedNodes = (*skippedNodes)[:length-1]
					if strings.HasSuffix(skippedNode.path, path) {
						path = skippedNode.path
						n = skippedNode.node
						if value.params != nil {
							*value.params = (*value.params)[:skippedNode.paramsCount]
						}
						globalParamsCount = skippedNode.paramsCount
						continue walk
					}
				}
				//	n = latestNode.children[len(latestNode.children)-1]
			}
			// 我们应该已经到达包含处理函数的节点。
			// 检查此节点是否注册了处理函数。
			if value.handlers = n.handlers; value.handlers != nil {
				value.fullPath = n.fullPath
				return value
			}

			// 如果没有找到此路由的处理程序，但此路由有一个通配符子节点，则必须有一个处理程序用于此路径并附加一个尾部斜杠
			if path == "/" && n.wildChild && n.nType != root {
				value.tsr = true
				return value
			}

			if path == "/" && n.nType == static {
				value.tsr = true
				return value
			}

			// 没有找到处理函数。检查是否存在该路径 + 尾部斜杠的处理函数以便进行尾部斜杠推荐
			for i, c := range []byte(n.indices) {
				if c == '/' {
					n = n.children[i]
					value.tsr = (len(n.path) == 1 && n.handlers != nil) ||
						(n.nType == catchAll && n.children[0].handlers != nil)
					return value
				}
			}

			return value
		}

		// 没有找到匹配项。如果存在该路径的叶子节点，我们可以推荐重定向到相同的 URL 并附加一个尾部斜杠
		value.tsr = path == "/" ||
			(len(prefix) == len(path)+1 && prefix[len(path)] == '/' &&
				path == prefix[:len(prefix)-1] && n.handlers != nil)

		// 回滚到最后一个有效的 skippedNode
		if !value.tsr && path != "/" {
			for length := len(*skippedNodes); length > 0; length-- {
				skippedNode := (*skippedNodes)[length-1]
				*skippedNodes = (*skippedNodes)[:length-1]
				if strings.HasSuffix(skippedNode.path, path) {
					path = skippedNode.path
					n = skippedNode.node
					if value.params != nil {
						*value.params = (*value.params)[:skippedNode.paramsCount]
					}
					globalParamsCount = skippedNode.paramsCount
					continue walk
				}
			}
		}

		return value
	}
}

// 对给定路径进行不区分大小写的查找，并尝试找到处理函数。
// 它还可以选择性地修正尾部斜杠。
// 它返回大小写正确的路径以及一个布尔值，表示查找是否成功。
func (n *node) findCaseInsensitivePath(path string, fixTrailingSlash bool) ([]byte, bool) {
	const stackBufSize = 128

	// 在常见情况下，在栈上使用静态大小的缓冲区。
	// 如果路径太长，则在堆上分配一个缓冲区。
	buf := make([]byte, 0, stackBufSize)
	if length := len(path) + 1; length > stackBufSize {
		buf = make([]byte, 0, length)
	}

	ciPath := n.findCaseInsensitivePathRec(
		path,
		buf,       // 预分配足够的内存用于新的路径
		[4]byte{}, // 空的 rune 缓冲区
		fixTrailingSlash,
	)

	return ciPath, ciPath != nil
}

// 将数组中的字节向左移动 n 个字节
func shiftNRuneBytes(rb [4]byte, n int) [4]byte {
	switch n {
	case 0:
		return rb
	case 1:
		return [4]byte{rb[1], rb[2], rb[3], 0}
	case 2:
		return [4]byte{rb[2], rb[3]}
	case 3:
		return [4]byte{rb[3]}
	default:
		return [4]byte{}
	}
}

// 递归的不区分大小写的查找函数，由 n.findCaseInsensitivePath 使用
func (n *node) findCaseInsensitivePathRec(path string, ciPath []byte, rb [4]byte, fixTrailingSlash bool) []byte {
	npLen := len(n.path)

walk: // 外部循环用于遍历树
	for len(path) >= npLen && (npLen == 0 || strings.EqualFold(path[1:npLen], n.path[1:])) {
		// 将公共前缀添加到结果中
		oldPath := path
		path = path[npLen:]
		ciPath = append(ciPath, n.path...)

		if len(path) == 0 {
			// 我们应该已经到达包含处理函数的节点。
			// 检查此节点是否注册了处理函数。
			if n.handlers != nil {
				return ciPath
			}

			// 没有找到处理函数。
			// 尝试通过添加尾部斜杠来修正路径
			if fixTrailingSlash {
				for i, c := range []byte(n.indices) {
					if c == '/' {
						n = n.children[i]
						if (len(n.path) == 1 && n.handlers != nil) ||
							(n.nType == catchAll && n.children[0].handlers != nil) {
							return append(ciPath, '/')
						}
						return nil
					}
				}
			}
			return nil
		}

		// 如果此节点没有通配符（参数或 catchAll）子节点，
		// 我们可以直接查找下一个子节点并继续向下遍历树
		if !n.wildChild {
			// 跳过已经处理的 rune 字节
			rb = shiftNRuneBytes(rb, npLen)

			if rb[0] != 0 {
				// 旧的 rune 尚未完成
				idxc := rb[0]
				for i, c := range []byte(n.indices) {
					if c == idxc {
						// 继续处理子节点
						n = n.children[i]
						npLen = len(n.path)
						continue walk
					}
				}
			} else {
				// 处理一个新的 rune
				var rv rune

				// 找到 rune 的起始位置。
				// runes 最多可以有 4 个字节，
				// -4 绝对会是另一个 rune。
				var off int
				for max_ := min(npLen, 3); off < max_; off++ {
					if i := npLen - off; utf8.RuneStart(oldPath[i]) {
						// 从缓存的路径中读取 rune
						rv, _ = utf8.DecodeRuneInString(oldPath[i:])
						break
					}
				}

				// 计算当前 rune 的小写字节
				lo := unicode.ToLower(rv)
				utf8.EncodeRune(rb[:], lo)

				// 跳过已经处理的字节
				rb = shiftNRuneBytes(rb, off)

				idxc := rb[0]
				for i, c := range []byte(n.indices) {
					// 小写匹配
					if c == idxc {
						// 必须使用递归方法，因为大写字节和小写字节
						// 可能都存在作为索引
						if out := n.children[i].findCaseInsensitivePathRec(
							path, ciPath, rb, fixTrailingSlash,
						); out != nil {
							return out
						}
						break
					}
				}

				// 如果我们没有找到匹配项，针对不同的情况尝试大写 rune
				if up := unicode.ToUpper(rv); up != lo {
					utf8.EncodeRune(rb[:], up)
					rb = shiftNRuneBytes(rb, off)

					idxc := rb[0]
					for i, c := range []byte(n.indices) {
						// 大写匹配
						if c == idxc {
							// 继续处理子节点
							n = n.children[i]
							npLen = len(n.path)
							continue walk
						}
					}
				}
			}

			// 未找到任何内容。如果该路径存在叶子节点，我们可以建议重定向到
			// 没有尾随斜杠的相同 URL
			if fixTrailingSlash && path == "/" && n.handlers != nil {
				return ciPath
			}
			return nil
		}

		n = n.children[0]
		switch n.nType {
		case param:
			// 找到参数的结束位置（要么是 '/' 要么是路径的结束）
			end := 0
			for end < len(path) && path[end] != '/' {
				end++
			}

			// 将参数值添加到不区分大小写的路径中
			ciPath = append(ciPath, path[:end]...)

			// 我们需要深入！
			if end < len(path) {
				if len(n.children) > 0 {
					// 继续处理子节点
					n = n.children[0]
					npLen = len(n.path)
					path = path[end:]
					continue
				}

				// ... 但是我们不能
				if fixTrailingSlash && len(path) == end+1 {
					return ciPath
				}
				return nil
			}

			if n.handlers != nil {
				return ciPath
			}

			if fixTrailingSlash && len(n.children) == 1 {
				// 未找到处理程序。检查此路径 + 尾随斜杠是否存在处理程序
				n = n.children[0]
				if n.path == "/" && n.handlers != nil {
					return append(ciPath, '/')
				}
			}

			return nil

		case catchAll:
			return append(ciPath, path...)

		default:
			panic("invalid node type")
		}
	}

	// 未找到任何内容。
	// 尝试通过添加/删除尾随斜杠来修复路径
	if fixTrailingSlash {
		if path == "/" {
			return ciPath
		}
		if len(path)+1 == npLen && n.path[len(path)] == '/' &&
			strings.EqualFold(path[1:], n.path[1:len(path)]) && n.handlers != nil {
			return append(ciPath, n.path...)
		}
	}
	return nil
}
