package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/allegro/bigcache"
	"github.com/miekg/dns"
)

var (
	domainsFile   = flag.String("i", "domains.txt", "File with domains")
	resolversFile = flag.String("r", "resolvers.txt", "File with resolvers")
	waitSecs      = flag.Int("t", 10, "Seconds to wait for incoming replies")
	throttleMSecs = flag.Int("throttle", 10, "Microseconds to wait before sending new packet to socket")
	rTypes        = flag.String("q", "A,AAAA,NS,MX,SOA,SRV", "Record types to query")
	retryAfter    = flag.Int("retryafter", 30, "Seconds to wait before packet resend")
	retryCount    = flag.Uint("retrycount", 2, "Times to resend a lost packet")
)

type retry struct {
	m     dns.Msg
	tries uint8
}

func main() {
	flag.Parse()

	conn, err := net.ListenUDP("udp", nil)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	retryRecords := make(chan retry, 10000)

	revertedDNSMap := make(map[string]uint16, len(dns.TypeToString))
	for k, v := range dns.TypeToString {
		revertedDNSMap[v] = k
	}
	types := csvToDNSTypes(*rTypes, revertedDNSMap)

	cacheConfig := bigcache.DefaultConfig(time.Duration(*retryAfter) * time.Second)
	cacheConfig.Shards = 65536
	cacheConfig.HardMaxCacheSize = 512
	cacheConfig.OnRemoveFilterSet(bigcache.Expired)
	cacheConfig.OnRemoveWithReason = func(key string, entry []byte, reason bigcache.RemoveReason) {
		if reason != bigcache.Expired {
			return
		}

		log.Printf("called on remove for %s - %s, reason: %s\n", key, string(entry), reason)
		if uint8(entry[0]) > uint8(*retryCount) {
			log.Printf("failed to resolve domain %s, tries: %d\n", key, uint8(entry[0]))
			return
		}

		m := &dns.Msg{}
		parts := strings.Split(key, "#")
		name, rtype := parts[0], parts[1]
		m.SetQuestion(name, revertedDNSMap[rtype])
		log.Printf("retrying %+v\n", m)
		retryRecords <- retry{*m, uint8(entry[0])}
	}
	cache, err := bigcache.NewBigCache(cacheConfig)
	if err != nil {
		log.Fatal(err)
	}

	go Listen(conn, retryRecords, cache)

	r, err := os.Open(*resolversFile)
	if err != nil {
		log.Fatal(err)
	}

	var resolvers []string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		resolvers = append(resolvers, scanner.Text())
	}

	rotateResolvers, err := NewResolvers(resolvers)
	if err != nil {
		log.Fatal(err)
	}

	f, err := os.Open(*domainsFile)
	if err != nil {
		log.Fatal(err)
	}

	cancel := make(chan os.Signal, 1)
	signal.Notify(cancel, syscall.SIGINT, syscall.SIGTERM)

	// ticker := time.NewTicker(time.Duration(*throttleMSecs) * time.Microsecond)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		waitTick := time.NewTicker(time.Duration(*waitSecs) * time.Second)
		rBuf := make([]byte, 4096)
		for {
			select {
			case retryRec := <-retryRecords:
				if retryRec.tries > uint8(*retryCount) {
					log.Printf("number of retries exceeded for %s\n", retryRec.m)
					continue
				}

				recID := recIDFromMsg(&retryRec.m)
				rBuf = rBuf[:0]
				// err = sendMsg(conn, rBuf, &retryRec.m, rotateResolvers(), ticker)
				err = sendMsg(conn, rBuf, &retryRec.m, rotateResolvers(), time.Duration(*throttleMSecs))
				if err != nil {
					log.Fatal(err)
				}
				cache.Set(recID, []byte{byte(retryRec.tries + 1)})

				waitTick = time.NewTicker(time.Duration(*waitSecs) * time.Second)
			case <-waitTick.C:
				log.Println(cache.Len())
				if cache.Len() > 0 {
					waitTick = time.NewTicker(time.Duration(*waitSecs) * time.Second)
					continue
				}
				log.Printf("wait time exceeded %d seconds, exiting", *waitSecs)
				waitTick.Stop()
				wg.Done()
				break
			case <-cancel:
				log.Println("ctrl+c")
				waitTick.Stop()
				os.Exit(1)
			}
		}
	}()

	sBuf := make([]byte, 4096)
	scanner = bufio.NewScanner(f)
	for scanner.Scan() {
		for i := range types {
			sMsg := &dns.Msg{}
			qname := dns.Fqdn(scanner.Text())
			sMsg.SetQuestion(qname, types[i])
			sBuf = sBuf[:0]
			err = sendMsg(conn, sBuf, sMsg, rotateResolvers(), time.Duration(*throttleMSecs))
			if err != nil {
				log.Fatal(err)
			}
			err = cache.Set(recIDFromMsg(sMsg), []byte{0})
			if err != nil {
				log.Fatal(err)
			}
		}
	}

	wg.Wait()
}

// NewResolvers return a function for rotating resolvers in round-robin fashion
// or a parsing error.
// Resolvers should be in the form: ip or ip:port. Only UDP resolvers are supported.
func NewResolvers(rs []string) (func() net.Addr, error) {
	var (
		addrs []*net.UDPAddr
		c     int
		mut   sync.Mutex
		addr  *net.UDPAddr
	)
	for i := range rs {

		parts := strings.Split(rs[i], ":")
		if len(parts) == 1 {
			rs[i] = rs[i] + ":53"
		}

		a, err := net.ResolveUDPAddr("udp", rs[i])
		if err != nil {
			return nil, err
		}
		addrs = append(addrs, a)
	}

	return func() net.Addr {
		mut.Lock()
		defer mut.Unlock()
		if c == len(addrs) {
			c = 0
		}
		addr = addrs[c]
		c++
		return addr
	}, nil
}

func csvToDNSTypes(s string, revertedMap map[string]uint16) []uint16 {
	var types []uint16
	qs := strings.Split(s, ",")

	for _, q := range qs {
		types = append(types, revertedMap[q])
	}

	return types
}

func sendMsg(conn net.PacketConn, buf []byte, m *dns.Msg, resolver net.Addr, throttle time.Duration) error {
	var err error

	mm := &dns.Msg{}
	mm.SetQuestion(m.Question[0].Name, m.Question[0].Qtype)

	buf, err = mm.Pack()
	if err != nil {
		return fmt.Errorf("dns.Msg packing %+v: %w", mm, err)
	}

	<-time.After(throttle * time.Microsecond)
	_, err = conn.WriteTo(buf, resolver)
	if err != nil {
		return fmt.Errorf("conn writing: %w", err)
	}

	return nil
}

// Listen for new packets and delete records from a cache.
func Listen(conn net.PacketConn, retryRecords chan retry, cache *bigcache.BigCache) {
	// var from net.Addr
	buf := make([]byte, 4096)
	for {
		m := new(dns.Msg)
		// _, from, err = conn.ReadFrom(buf)
		n, _, err := conn.ReadFrom(buf[:cap(buf)])
		if err, ok := err.(net.Error); ok && err.Timeout() {
			log.Println(err)
			continue
		}
		// log.Printf("read %d bytes\n", n)

		if err == io.EOF {
			break
		}
		buf = buf[:n]

		err = m.Unpack(buf)
		if err != nil {
			log.Fatal(err)
		}

		id := recIDFromMsg(m)

		if len(m.Answer) == 0 && len(m.Ns) == 0 && len(m.Extra) == 0 {
			// if m.Rcode != dns.RcodeSuccess || m.Rcode != dns.RcodeNameError {
			// log.Printf("errored record from %s: %+v\n", from, m)
			count, err := cache.Get(id)
			if err != nil {
				log.Printf("no value %s\n", id)
				continue
			}
			log.Printf("empty message, retrying %+v\n", m)
			retryRecords <- retry{*m, uint8(count[0])}
			continue
		}

		err = cache.Delete(id)
		if err == bigcache.ErrEntryNotFound {
			log.Printf("tried to remove deleted item %s\n", id)
		}
		fmt.Println(m)
	}
}

func recIDFromMsg(m *dns.Msg) string {
	return m.Question[0].Name + "#" + dns.TypeToString[m.Question[0].Qtype]
}
