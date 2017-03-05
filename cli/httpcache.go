package main

import (
	"flag"
	"log"
	"net/http"
	"os"

	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"github.com/elazarl/goproxy"
	"github.com/fkautz/tigerbat/cache/diskcache"
	"github.com/gorilla/handlers"
	"github.com/lox/httpcache"
	"github.com/lox/httpcache/httplog"
	"io/ioutil"
)

const (
	defaultListen = "0.0.0.0:8080"
	defaultDir    = "./cachedata"
)

var (
	listen   string
	useDisk  bool
	private  bool
	dir      string
	dumpHttp bool
	verbose  bool
)

func init() {
	flag.StringVar(&listen, "listen", defaultListen, "the host and port to bind to")
	flag.StringVar(&dir, "dir", defaultDir, "the dir to store cache data in, implies -disk")
	flag.BoolVar(&useDisk, "disk", false, "whether to store cache data to disk")
	flag.BoolVar(&verbose, "v", false, "show verbose output and debugging")
	flag.BoolVar(&private, "private", false, "make the cache private")
	flag.BoolVar(&dumpHttp, "dumphttp", false, "dumps http requests and responses to stdout")
	flag.Parse()

	if verbose {
		httpcache.DebugLogging = true
		log.SetFlags(log.Flags() | log.Lshortfile)
	}
}

func main() {
	proxy := goproxy.NewProxyHttpServer()
	proxy.Verbose = true

	var cache httpcache.Cache

	if useDisk && dir != "" {
		log.Printf("storing cached resources in %s", dir)
		if err := os.MkdirAll(dir, 0700); err != nil {
			log.Fatal(err)
		}
		var err error
		cache, err = newTigerBatDiskCache()
		if err != nil {
			log.Fatal(err)
		}
		log.Println("Instantiated tigerbat cache")
	} else {
		cache = httpcache.NewMemoryCache()
	}

	handler := httpcache.NewHandler(cache, proxy)
	handler.Shared = !private

	respLogger := httplog.NewResponseLogger(handler)
	respLogger.DumpRequests = dumpHttp
	respLogger.DumpResponses = dumpHttp
	respLogger.DumpErrors = dumpHttp

	log.Printf("listening on http://%s", listen)
	log.Fatal(http.ListenAndServe(listen, handlers.LoggingHandler(os.Stderr, respLogger)))
}

type TigerBatDiskCache struct {
	cache diskcache.Cache
}

func newTigerBatDiskCache() (*TigerBatDiskCache, error) {
	err := os.MkdirAll("./tigerbatcache", 0700)
	if err != nil {
		log.Println(err)
		return nil, err
	}
	cache, err := diskcache.New("tigerbatcache", 8*1024*1024*1024, 7*1024*1024*1024)
	if err != nil {
		log.Println(err)
		return nil, err
	}
	return &TigerBatDiskCache{
		cache: cache,
	}, nil
}

func (tigerbat *TigerBatDiskCache) Header(key string) (httpcache.Header, error) {
	resourceKey, _ := getKeys(key)
	reader, err := tigerbat.cache.Get(resourceKey)
	if err != nil {
		return httpcache.Header{}, err
	}
	defer reader.Close()

	decoder := gob.NewDecoder(reader)

	header := httpcache.Header{}

	err = decoder.Decode(&header)
	if err != nil {
		return httpcache.Header{}, err
	}

	return header, nil
}

func (tigerbat *TigerBatDiskCache) Store(res *httpcache.Resource, keys ...string) error {
	resourceBuffer := bytes.Buffer{}
	encoder := gob.NewEncoder(&resourceBuffer)
	statusHeader := httpcache.Header{
		StatusCode: res.Status(),
		Header:     res.Header(),
	}
	err := encoder.Encode(&statusHeader)
	if err != nil {
		return err
	}
	body, err := ioutil.ReadAll(res)
	if err != nil {
		return err
	}
	for _, key := range keys {
		resourceKey, bodyKey := getKeys(key)
		err := tigerbat.cache.Put(resourceKey, bytes.NewBuffer(resourceBuffer.Bytes()))
		if err != nil {
			return err
		}
		tigerbat.cache.Put(bodyKey, bytes.NewBuffer(body))
		if err != nil {
			return err
		}
	}
	return nil
}
func (tigerbat *TigerBatDiskCache) Retrieve(key string) (*httpcache.Resource, error) {
	resourceKey, bodyKey := getKeys(key)
	resourceReader, err := tigerbat.cache.Get(resourceKey)
	if err != nil {
		tigerbat.cache.Remove(resourceKey)
		log.Println(err)
		return nil, httpcache.ErrNotFoundInCache
	}
	defer resourceReader.Close()

	bodyReader, err := tigerbat.cache.GetFile(bodyKey)
	if err != nil {
		tigerbat.cache.Remove(bodyKey)
		log.Println(err)
		return nil, err
	}

	resourceDecoder := gob.NewDecoder(resourceReader)
	statusHead := httpcache.Header{}
	resourceDecoder.Decode(&statusHead)

	resource := httpcache.NewResource(statusHead.StatusCode, bodyReader, statusHead.Header)

	return resource, nil
}
func (tigerbat *TigerBatDiskCache) Invalidate(keys ...string) {
	for _, key := range keys {
		tigerbat.cache.Remove(key)
	}
}
func (tigerbat *TigerBatDiskCache) Freshen(res *httpcache.Resource, keys ...string) error {
	for _, key := range keys {
		resourceKey, bodyKey := getKeys(key)
		tigerbat.cache.Remove(resourceKey)
		tigerbat.cache.Remove(bodyKey)
	}
	return tigerbat.Store(res, keys...)
}

func getKeys(key string) (string, string) {
	resourceHash := sha256.Sum256([]byte(key + "#resource"))
	bodyHash := sha256.Sum256([]byte(key + "#body"))
	resourceKey := hex.EncodeToString(resourceHash[:])
	bodyKey := hex.EncodeToString(bodyHash[:])
	return resourceKey, bodyKey
}
