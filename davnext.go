package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"regexp"
	"strings"

	"golang.org/x/net/webdav"
)

func main() {
	var listen, prefix, username, password, dir, next string
	var modify, help bool
	flag.StringVar(&listen, "listen", ":80", "Listen is webdav listen port")
	flag.StringVar(&prefix, "prefix", "", "Prefix is the URL path prefix to strip from WebDAV resource paths")
	flag.StringVar(&username, "username", "", "basic auth username")
	flag.StringVar(&password, "password", "", "basic auth password")
	flag.StringVar(&dir, "dir", ".", "Dir filesystem folder")
	flag.BoolVar(&modify, "modify", false, "Dir filesystem is readonly")
	flag.StringVar(&next, "next", "", "Next webdav url")
	flag.BoolVar(&help, "help", false, "show help")
	flag.Parse()
	if help {
		flag.PrintDefaults()
		return
	}
	var err error
	var nextURL *url.URL
	if len(next) > 0 {
		nextURL, err = url.Parse(next)
		if err != nil {
			panic(err)
		}
	}
	handler := &webdav.Handler{
		Prefix:     prefix,
		FileSystem: NewDir(dir, modify),
		LockSystem: webdav.NewMemLS(),
	}
	hander := NewHandler(handler, nextURL)
	hander.Username = username
	hander.Password = password
	log.Printf("listen webdav on %v\n", listen)
	http.ListenAndServe(listen, hander)
}

type InterceptWriter struct {
	Base       http.ResponseWriter
	StatusCode int
}

func (i *InterceptWriter) Header() http.Header {
	if i.StatusCode == http.StatusNotFound {
		return http.Header{}
	}
	return i.Base.Header()
}

func (i *InterceptWriter) Write(b []byte) (int, error) {
	os.Stdout.Write(b)
	if i.StatusCode == http.StatusNotFound {
		return len(b), nil
	}
	return i.Base.Write(b)
}

func (i *InterceptWriter) WriteHeader(statusCode int) {
	i.StatusCode = statusCode
	if i.StatusCode == http.StatusNotFound {
		return
	}
	i.Base.WriteHeader(statusCode)
}

type CacheWriter struct {
	StatusCode int
	H          http.Header
	B          *bytes.Buffer
}

func NewCacheWriter() (writer *CacheWriter) {
	writer = &CacheWriter{
		H: http.Header{},
		B: bytes.NewBuffer(nil),
	}
	return
}

func (c *CacheWriter) Header() http.Header {
	return c.H
}

func (c *CacheWriter) Write(b []byte) (int, error) {
	return c.B.Write(b)
}

func (c *CacheWriter) WriteHeader(statusCode int) {
	c.StatusCode = statusCode
}

type Dir struct {
	webdav.Dir
	Modify bool
}

func NewDir(base string, modify bool) (dir *Dir) {
	dir = &Dir{
		Dir:    webdav.Dir(base),
		Modify: modify,
	}
	return
}

func (d *Dir) Mkdir(ctx context.Context, name string, perm os.FileMode) (err error) {
	if !d.Modify {
		err = os.ErrPermission
		return
	}
	err = d.Dir.Mkdir(ctx, name, perm)
	return
}
func (d *Dir) RemoveAll(ctx context.Context, name string) (err error) {
	if !d.Modify {
		err = os.ErrPermission
		return
	}
	err = d.Dir.RemoveAll(ctx, name)
	return
}
func (d *Dir) Rename(ctx context.Context, oldName, newName string) (err error) {
	if !d.Modify {
		err = os.ErrPermission
		return
	}
	err = d.Dir.Rename(ctx, oldName, newName)
	return
}

type Handler struct {
	Dav      *webdav.Handler
	Username string
	Password string
	Next     *url.URL
	proxy    *httputil.ReverseProxy
}

func NewHandler(dav *webdav.Handler, next *url.URL) (handler *Handler) {
	handler = &Handler{
		Dav:  dav,
		Next: next,
	}
	if next != nil {
		handler.proxy = httputil.NewSingleHostReverseProxy(next)
		director := handler.proxy.Director
		handler.proxy.Director = func(r *http.Request) {
			director(r)
			r.Host = next.Host
		}
	}
	return
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Printf("Handler %v %v by %v\n", r.Method, r.URL, r.Host)
	if len(h.Username) > 0 {
		username, password, _ := r.BasicAuth()
		if username != h.Username || password != h.Password {
			log.Printf("Local process %v is unauthorized by %v,%v", r.URL.Path, username, password)
			w.Header().Set("WWW-Authenticate", "Basic realm=Dav Server")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, "unauthorized")
			return
		}
	}
	if h.Next == nil {
		h.Dav.ServeHTTP(w, r)
		return
	}
	username := h.Next.User.Username()
	passsword, _ := h.Next.User.Password()
	if len(username) > 0 {
		r.SetBasicAuth(username, passsword)
	}
	body, _ := ioutil.ReadAll(r.Body)
	r1 := r.Clone(context.Background())
	if len(body) > 0 {
		r1.Body = NewBodyReader(body)
	}
	r2 := r.Clone(context.Background())
	r2.Header.Set("Host", h.Next.Host)
	if len(body) > 0 {
		r2.Body = NewBodyReader(body)
	}
	if r.Method == "PROPFIND" {
		localWriter := NewCacheWriter()
		remoteWriter := NewCacheWriter()
		h.Dav.ServeHTTP(localWriter, r1)
		h.proxy.ServeHTTP(remoteWriter, r2)
		if localWriter.StatusCode != http.StatusMultiStatus {
			log.Printf("Local process %v %v to %v is fault with %v", r.Method, r.URL, h.Next, localWriter.B.String())
		}
		if remoteWriter.StatusCode != http.StatusMultiStatus {
			log.Printf("Next process %v %v to %v is fault with %v", r.Method, r.URL, h.Next, remoteWriter.B.String())
		}
		if localWriter.StatusCode != http.StatusMultiStatus && remoteWriter.StatusCode != http.StatusMultiStatus {
			for k, v := range localWriter.H {
				w.Header()[k] = v
			}
			w.WriteHeader(localWriter.StatusCode)
			w.Write(localWriter.B.Bytes())
			return
		}
		propfind := NewPropfind()
		if localWriter.StatusCode == http.StatusMultiStatus {
			propfind.Append(localWriter.B.String())
		}
		if remoteWriter.StatusCode == http.StatusMultiStatus {
			propfind.Append(remoteWriter.B.String())
		}
		buffer := bytes.NewBuffer(nil)
		propfind.WriteTo(buffer)
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		w.Header().Set("Content-Length", fmt.Sprintf("%v", buffer.Len()))
		w.WriteHeader(http.StatusMultiStatus)
		propfind.WriteTo(w)
		return
	}
	writer := &InterceptWriter{Base: w}
	h.Dav.ServeHTTP(writer, r1)
	if writer.StatusCode != http.StatusNotFound {
		return
	}
	log.Printf("Next process %v to %v\n", r.URL.Path, h.Next)
	h.proxy.ServeHTTP(w, r2)
}

type BodyReader struct {
	*bytes.Buffer
}

func NewBodyReader(data []byte) (reader *BodyReader) {
	reader = &BodyReader{
		Buffer: bytes.NewBuffer(data),
	}
	return
}

func (b *BodyReader) Close() (err error) {
	return
}

type Propfind struct {
	items    map[string]string
	response *regexp.Regexp
	href     *regexp.Regexp
}

func NewPropfind() (propfind *Propfind) {
	propfind = &Propfind{}
	propfind.items = map[string]string{}
	propfind.response = regexp.MustCompile(`(?Us)<D:response>.*</D:response>`)
	propfind.href = regexp.MustCompile(`(?Us)<D:href>.*</D:href>`)
	return
}

func (p *Propfind) Append(data string) {
	p.response.ReplaceAllStringFunc(data, func(s string) string {
		href := p.href.FindString(s)
		href = strings.TrimPrefix(href, "<D:href>")
		href = strings.TrimSuffix(href, "</D:href>")
		href = strings.TrimSpace(href)
		if _, ok := p.items[href]; !ok {
			p.items[href] = s
		}
		return ""
	})
}

func (p *Propfind) WriteTo(w io.Writer) (n int64, err error) {
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><D:multistatus xmlns:D="DAV:">`)
	for _, item := range p.items {
		fmt.Fprintf(w, "%v", item)
	}
	fmt.Fprintf(w, `</D:multistatus>`)
	return
}
