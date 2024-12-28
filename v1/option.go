package alert

import (
	"github.com/bww/go-router/v2"
)

type Option func(c Context) Context

type Context struct {
	Request *router.Request
	Tags    Tags
	Extra   map[string]interface{}
}

func WithRequest(req *router.Request) Option {
	return func(c Context) Context {
		c.Request = req
		return c
	}
}

func WithTags(tags Tags) Option {
	return func(c Context) Context {
		c.Tags = tags
		return c
	}
}

func WithExtra(extra map[string]interface{}) Option {
	return func(c Context) Context {
		c.Extra = extra
		return c
	}
}
