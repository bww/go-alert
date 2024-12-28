package alert

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"sync"

	"github.com/bww/go-ident/v1"
	"github.com/bww/go-util/v1/debug"
	errutil "github.com/bww/go-util/v1/errors"
	"github.com/getsentry/sentry-go"
)

var shared *Alerter
var lock sync.Mutex

var (
	ErrReinitialized = errors.New("Cannot initialize more than once")
	ErrUnavailable   = errors.New("Unavailable")
)

const maxErrorDepth = 3

type Tags map[string]interface{}

type Config struct {
	Sentry    *sentry.Client
	Logger    *slog.Logger
	Channel   ident.Ident
	Component string
	Hostname  string
	Verbose   bool
}

func Init(conf Config) {
	lock.Lock()
	defer lock.Unlock()
	var err error
	if shared != nil {
		panic(ErrReinitialized)
	}
	shared, err = New(conf)
	if err != nil {
		panic(err)
	}
}

func Default() *Alerter {
	return shared
}

func Errorf(f string, args ...interface{}) {
	lock.Lock()
	defer lock.Unlock()
	if shared != nil {
		shared.Errorf(f, args...)
	}
}

func Error(err error, opts ...Option) {
	lock.Lock()
	defer lock.Unlock()
	if shared != nil {
		shared.Error(err, opts...)
	}
}

type Alerter struct {
	sentry    *sentry.Client
	log       *slog.Logger
	channel   ident.Ident
	component string
	hostname  string
	verbose   bool
}

func New(conf Config) (*Alerter, error) {
	if conf.Sentry != nil {
		hub := sentry.CurrentHub()
		hub.BindClient(conf.Sentry)
		scope := hub.Scope()
		if conf.Component != "" {
			scope.SetTag("component", conf.Component)
		}
		if conf.Hostname != "" {
			scope.SetTag("host", conf.Hostname)
		}
	}

	if conf.Logger != nil {
		if conf.Component != "" {
			conf.Logger = conf.Logger.With("component", conf.Component)
		}
		if conf.Hostname != "" {
			conf.Logger = conf.Logger.With("host", conf.Hostname)
		}
	}

	return &Alerter{
		sentry:    conf.Sentry,
		log:       conf.Logger,
		channel:   conf.Channel,
		component: conf.Component,
		hostname:  conf.Hostname,
		verbose:   conf.Verbose,
	}, nil
}

func (a *Alerter) Errorf(f string, args ...interface{}) {
	a.Error(fmt.Errorf(f, args...))
}

func (a *Alerter) Error(err error, opts ...Option) {
	ref := errutil.Refstr(err)

	var h *sentry.Hub
	if a.sentry != nil {
		h = sentry.CurrentHub().Clone()
	}
	var log *slog.Logger
	if a.verbose && a.log != nil {
		log = a.log.With("alert", "error")
		if ref != "" {
			log = log.With("ref", ref)
		}
	}

	var cxt Context
	for _, o := range opts {
		cxt = o(cxt)
	}

	if req := cxt.Request; req != nil {
		if h != nil {
			h.Scope().SetRequest((*http.Request)(req))
			h.Scope().SetUser(sentry.User{IPAddress: req.OriginAddr()})
		}
		if log != nil {
			log = log.With(
				"alert", "error",
				"request", fmt.Sprintf("%s %s", req.Method, req.URL.String()),
			)
		}
	}

	if tags := cxt.Tags; len(tags) > 0 {
		if h != nil {
			s := h.Scope()
			for k, v := range tags {
				s.SetTag(k, fmt.Sprint(v))
			}
			if ref != "" {
				s.SetTag("ref", ref)
			}
		}
		if log != nil {
			for k, v := range tags {
				log = log.With(k, v)
			}
		}
	}

	if extra := cxt.Extra; len(extra) > 0 {
		if log != nil {
			for k, v := range extra {
				log = log.With(k, v)
			}
		}
	}

	if h != nil {
		a.captureError(h, err, cxt.Extra)
	}
	if log != nil && a.verbose {
		log.Error(err.Error())
	}
}

func (a *Alerter) captureError(hub *sentry.Hub, err error, extra map[string]interface{}) {
	hub.CaptureEvent(a.eventFromError(err, sentry.LevelError, extra))
}

func (a *Alerter) eventFromError(err error, lvl sentry.Level, extra map[string]interface{}) *sentry.Event {
	event := sentry.NewEvent()
	event.Level = lvl
	event.Extra = extra

	if c, ok := err.(interface{ Title() string }); ok {
		event.Message = c.Title()
	}

	var stack *sentry.Stacktrace
	for i := 0; i < maxErrorDepth && err != nil; i++ {
		err, stack = extractStacktrace(err)
		event.Exception = append(event.Exception, sentry.Exception{
			Value:      err.Error(),
			Type:       reflect.TypeOf(err).String(),
			Stacktrace: stack,
		})
		switch prev := err.(type) {
		case interface{ Unwrap() error }:
			err = prev.Unwrap()
		case interface{ Cause() error }:
			err = prev.Cause()
		default:
			err = nil
		}
	}

	reverse(event.Exception)
	return event
}

func extractStacktrace(err error) (error, *sentry.Stacktrace) {
	switch c := err.(type) {
	case interface{ Frames() []debug.Frame }:
		return maybeUnwrap(err), convertStacktrace(c.Frames())
	default:
		return err, sentry.ExtractStacktrace(err)
	}
}

func maybeUnwrap(err error) error {
	switch c := err.(type) {
	case interface{ Unwrap() error }:
		return c.Unwrap()
	default:
		return err
	}
}

func convertStacktrace(frames []debug.Frame) *sentry.Stacktrace {
	conv := make([]sentry.Frame, len(frames))
	for i, e := range frames {
		conv[len(frames)-i-1] = sentry.Frame{
			Lineno:   e.Line,
			Filename: e.File,
			AbsPath:  e.Path,
			Function: e.Name,
		}
	}
	return &sentry.Stacktrace{
		Frames: conv,
	}
}

// reverse reverses the slice a in place.
func reverse(a []sentry.Exception) {
	for i := len(a)/2 - 1; i >= 0; i-- {
		opp := len(a) - 1 - i
		a[i], a[opp] = a[opp], a[i]
	}
}
