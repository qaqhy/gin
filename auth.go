// Copyright 2014 Manu Martinez-Almeida. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package gin

import (
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin/internal/bytesconv"
)

// AuthUserKey 是基本认证中用于用户凭证的 cookie 名称。
const AuthUserKey = "user"

// AuthProxyUserKey 是代理基本认证中用于 proxy_user 凭证的 cookie 名称。
const AuthProxyUserKey = "proxy_user"

// Accounts 定义了一个键值对，用于存储授权登录的用户名和密码。
type Accounts map[string]string

// authPair 结构体包含了一个认证值和用户名。
type authPair struct {
	value string
	user  string
}

// authPairs 是 authPair 的切片。
type authPairs []authPair

// searchCredential 方法在 authPairs 中搜索匹配的凭证。
func (a authPairs) searchCredential(authValue string) (string, bool) {
	if authValue == "" {
		return "", false
	}
	for _, pair := range a {
		// 使用恒定时间比较函数比较认证值
		if subtle.ConstantTimeCompare(bytesconv.StringToBytes(pair.value), bytesconv.StringToBytes(authValue)) == 1 {
			return pair.user, true
		}
	}
	return "", false
}

// BasicAuthForRealm 返回一个基本HTTP授权中间件。它接受一个 map[string]string 作为参数，
// 其中键是用户名，值是密码，以及 Realm 的名称。
// 如果 realm 为空，则默认使用 "Authorization Required"。
// (参见 http://tools.ietf.org/html/rfc2617#section-1.2)
func BasicAuthForRealm(accounts Accounts, realm string) HandlerFunc {
	if realm == "" {
		realm = "Authorization Required"
	}
	realm = "Basic realm=" + strconv.Quote(realm)
	pairs := processAccounts(accounts)
	return func(c *Context) {
		// 在允许的凭证列表中搜索用户
		user, found := pairs.searchCredential(c.requestHeader("Authorization"))
		if !found {
			// 凭证不匹配，返回401状态码并中止处理链。
			c.Header("WWW-Authenticate", realm)
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}

		// 找到用户凭证，将用户ID设置到上下文中的 AuthUserKey 键，稍后可以使用 c.MustGet(gin.AuthUserKey) 读取用户ID。
		c.Set(AuthUserKey, user)
	}
}

// BasicAuth 返回一个基本HTTP授权中间件。它接受一个 map[string]string 作为参数，
// 其中键是用户名，值是密码。
func BasicAuth(accounts Accounts) HandlerFunc {
	return BasicAuthForRealm(accounts, "")
}

func processAccounts(accounts Accounts) authPairs {
	length := len(accounts)
	assert1(length > 0, "Empty list of authorized credentials")
	pairs := make(authPairs, 0, length)
	for user, password := range accounts {
		assert1(user != "", "User can not be empty")
		value := authorizationHeader(user, password)
		pairs = append(pairs, authPair{
			value: value,
			user:  user,
		})
	}
	return pairs
}

func authorizationHeader(user, password string) string {
	base := user + ":" + password
	return "Basic " + base64.StdEncoding.EncodeToString(bytesconv.StringToBytes(base))
}

// BasicAuthForProxy 返回一个基本 HTTP 代理授权中间件。
// 如果 realm 为空，则默认使用 "Proxy Authorization Required"。
func BasicAuthForProxy(accounts Accounts, realm string) HandlerFunc {
	if realm == "" {
		realm = "Proxy Authorization Required"
	}
	realm = "Basic realm=" + strconv.Quote(realm)
	pairs := processAccounts(accounts)
	return func(c *Context) {
		proxyUser, found := pairs.searchCredential(c.requestHeader("Proxy-Authorization"))
		if !found {
			// 凭证不匹配，返回407状态码并中止处理链。
			c.Header("Proxy-Authenticate", realm)
			c.AbortWithStatus(http.StatusProxyAuthRequired)
			return
		}
		// 找到 proxy_user 的凭证，将 proxy_user 的 ID 设置到上下文中的 AuthProxyUserKey 键，以后可以使用
		// c.MustGet(gin.AuthProxyUserKey) 来读取 proxy_user 的 ID。
		c.Set(AuthProxyUserKey, proxyUser)
	}
}
