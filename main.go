package main

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
)

var (
	upstreamProxy    = `localhost:8001`
	upstreamProxyUrl *url.URL
	transport        http.Transport
)

func main() {
	if upstreamProxy != `` {
		url, err := url.Parse(fmt.Sprintf(`http://%s`, upstreamProxy))
		if err != nil {
			panic(err)
		}
		upstreamProxyUrl = url
	}
	transport = *(http.DefaultTransport).(*http.Transport)
	transport.Proxy = func(*http.Request) (*url.URL, error) {
		return upstreamProxyUrl, nil
	}

	http.ListenAndServe(`:8008`, http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		if req.Method == `CONNECT` {
			HandleConnect(res, req)
		} else {
			HandleHttp(res, req)
		}
		res.Write([]byte(""))
	}))
}

func WriteError(res http.ResponseWriter, pattern string, args ...any) {
	res.WriteHeader(http.StatusServiceUnavailable)
	msg := fmt.Sprintf(pattern, args...)
	log.Printf(`[ERR] %s`, msg)
	res.Write([]byte(msg))
}

func direct(req *http.Request) (conn net.Conn, err error) {
	conn, err = net.Dial(`tcp`, req.RequestURI)
	log.Printf(`Connected %s`, req.RequestURI)
	return
}

func mustWrite(conn net.Conn, s string) error {
	buf := []byte(s)
	n, err := conn.Write(buf)
	if n != len(buf) {
		panic(`Partial buf sent`)
	}
	return err
}

func readCharToBuf(conn net.Conn, result []byte) ([]byte, error) {
	buf := []byte{0}
	_, err := conn.Read(buf)
	if err != nil {
		return result, err
	}
	result = append(result, buf[0])
	return result, nil
}

func mustReadHttp(conn net.Conn) (string, error) {
	result := make([]byte, 0, 64)
	var err error
	for {
		result, err = readCharToBuf(conn, result)
		if err != nil {
			return ``, err
		}
		if len(result) >= 4 &&
			result[len(result)-4] == 13 && result[len(result)-3] == 10 &&
			result[len(result)-2] == 13 && result[len(result)-1] == 10 {
			return string(result[0 : len(result)-4]), nil
		}
	}
}

func httpsProxy(req *http.Request) (net.Conn, error) {
	if upstreamProxy == `` {
		return direct(req)
	}

	conn, err := net.Dial(`tcp`, upstreamProxy)
	if err != nil {
		return nil, err
	}

	err = mustWrite(conn, fmt.Sprintf(`CONNECT %s %s

`, req.RequestURI, req.Proto))
	if err != nil {
		conn.Close()
		return nil, err
	}
	status, err := mustReadHttp(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	log.Printf(`Proxy to %s via %s: %s`, req.RequestURI, upstreamProxy, status)
	return conn, err
}

func HandleConnect(res http.ResponseWriter, req *http.Request) {
	log.Printf("%s %s %s\n", req.Method, req.RequestURI, req.Proto)
	conn, err := httpsProxy(req)
	if err != nil {
		log.Printf(`[ERR] connect to %s: %s`, req.RequestURI, err.Error())
		WriteError(res, `Cannot connect to %s: %s`, req.RequestURI, err.Error())
		return
	}
	res.WriteHeader(http.StatusOK)

	hijacker := res.(http.Hijacker)
	if hijacker == nil {
		panic(`hijacker not possible`)
	}
	client, _, err := hijacker.Hijack()
	if err != nil {
		panic(`Hijack failed`)
	}

	go transfer(req.RequestURI+` client`, client, conn)
	go transfer(req.RequestURI+` server`, conn, client)
}

func transfer(name string, src, dest net.Conn) {
	// defer func() {
	// 	log.Printf(`%s disconnected`, name)
	// }()
	defer src.Close()
	defer dest.Close()
	io.Copy(dest, src)
}

func HandleHttp(w http.ResponseWriter, req *http.Request) {
	log.Printf(`%s %s %s`, req.Method, req.URL, req.Proto)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		WriteError(w, `Failed to request %s: %s`, req.URL, err.Error())
		return
	}
	defer resp.Body.Close()
	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}
