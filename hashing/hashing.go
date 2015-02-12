package main

import (
	"crypto/md5"
	"fmt"
	"log"
	"strings"
)

type Node struct {
	Server   string
	Instance string
}

type RingEntry struct {
	position int
	node     Node
}

type HashRing struct {
	ring     []RingEntry
	nodes    []Node
	replicas int
}

func NewHashRing() *HashRing {
	var chr = new(HashRing)
	chr.ring = make([]RingEntry, 0, 10)
	chr.nodes = make([]Node, 0, 10)
	chr.replicas = 100

	return chr
}

// NewNode returns a node object setup with the given string string and
// instance string.  None or empty instances should be represented by ""
func NewNode(server, instance string) (n Node) {
	n.Server = server
	n.Instance = instance
	return n
}

// computeRingPosition takes a string and computes where that string lives in
// the 16bit wide hash ring.
func computeRingPosition(key string) (result int) {
	// digest is our full 64bit hash as a slice of 8 bytes
	digest := md5.Sum([]byte(key))

	// Make an int out of the last 2 bytes for a 16bit ring
	for _, v := range digest[:2] {
		result = (result << 8) + int(v)
	}
	return
}

// bisect returns the insertion index where e should be inserted into ring
// if duplicate e's are already in the list the insertion point will be to the
// left or before the equal entries.
func bisect(ring []RingEntry, e RingEntry) (i int) {
	for i = 0; i < len(ring); i++ {
		if ring[i].position >= e.position {
			break
		}
	}

	return i
}

// insertRing inserts a RingEntry e into the slice ring in the correct
// order.  An updated []RingEntry slice is returned
func insertRing(ring []RingEntry, e RingEntry) []RingEntry {
	// Find where e goes in the ring
	i := bisect(ring, e)

	// Extend the underlying array if needed
	ring = append(ring, e)

	if i == len(ring)-1 {
		// last element in slice, or slice was empty, we are done
		return ring
	} else {
		// Move data around to make room for e
		copy(ring[i+1:], ring[i:len(ring)-1])
		ring[i] = e
	}
	return ring
}

// Node.KeyValue generates the string representation used in the hash
// ring just as Graphite's Python code does
func (t Node) KeyValue() string {
	if t.Instance == "" {
		return fmt.Sprintf("('%s', None)", t.Server)
	}
	return fmt.Sprintf("('%s', '%s')", t.Server, t.Instance)
}

// Node.String returns a string representation of the Node struct
func (t Node) String() string {
	if t.Instance == "" {
		return fmt.Sprintf("%s", t.Server)
	}
	return fmt.Sprintf("%s:%s", t.Server, t.Instance)
}

func (t *HashRing) String() string {
	servers := make([]string, 0)

	for i := 0; i < len(t.nodes); i++ {
		servers = append(servers, t.nodes[i].String())
	}
	return fmt.Sprintf("[HashRing: %d nodes, %d replicas, %d ring members %s]",
		len(t.nodes), t.replicas, len(t.ring), strings.Join(servers, " "))
}

func (t *HashRing) Replicas() int {
	return t.replicas
}

func (t *HashRing) SetReplicas(r int) {
	t.replicas = r
}

func (t *HashRing) AddNode(node Node) {
	t.nodes = append(t.nodes, node)
	for i := 0; i < t.replicas; i++ {
		var e RingEntry
		replica_key := fmt.Sprintf("%s:%d", node.KeyValue(), i)
		e.position = computeRingPosition(replica_key)
		e.node = node
		t.ring = insertRing(t.ring, e)
	}
}

func (t *HashRing) RemoveNode(node Node) {
	var i int

	// Find node in nodes
	for i = 0; i < len(t.nodes); {
		if node.String() == t.nodes[i].String() {
			t.nodes = append(t.nodes[:i], t.nodes[i+1:]...)
		} else {
			i++
		}
	}

	// Remove matching ring locations
	for i = 0; i < len(t.ring); {
		if node.String() == t.ring[i].node.String() {
			t.ring = append(t.ring[:i], t.ring[i+1:]...)
		} else {
			i++
		}
	}
}

func (t *HashRing) GetNode(key string) Node {
	if len(t.ring) == 0 {
		panic("HashRing is empty")
	}

	e := RingEntry{computeRingPosition(key), NewNode(key, "")}
	i := bisect(t.ring, e)
	//log.Printf("len(ring) = %d", len(t.ring))
	//log.Printf("Bisect index for %s is %d", key, i)
	//log.Printf("Ring position for %s is %x", key, e.position)
	return t.ring[i].node
}

func (t *HashRing) GetNodes(key string) []Node {
	if len(t.ring) == 0 {
		panic("HashRing is empty")
	}

	result := make([]Node, 0)
	seen := make(map[string]bool)
	e := RingEntry{computeRingPosition(key), NewNode(key, "")}
	index := bisect(t.ring, e)
	last := index - 1

	for len(seen) < len(t.nodes) && index != last {
		next := t.ring[index]
		if !seen[next.node.String()] {
			seen[next.node.String()] = true
			result = append(result, next.node)
		}
		index = (index + 1) % len(t.ring)
	}

	return result
}

func main() {
	log.Printf("Hello, test Graphite hashring package.")

	chr := NewHashRing()
	if chr.Replicas() == 100 {
		chr.SetReplicas(42)
	}

	log.Printf("ToString: %s", chr)

	log.Printf("Hexdigest: %x", md5.Sum([]byte("graphite010-g5")))
	log.Printf("Ring position of \"graphite010-g5\": %x", computeRingPosition("graphite010-g5"))

	chr.SetReplicas(100)
	log.Printf("Inserting ring members...")
	log.Printf("%s", chr)
	chr.AddNode(NewNode("graphite010-g5", ""))
	log.Printf("%s", chr)
	chr.AddNode(NewNode("graphite011-g5", "a"))
	log.Printf("%s", chr)
	chr.AddNode(NewNode("graphite012-g5", "b"))
	log.Printf("%s", chr)
	chr.AddNode(NewNode("graphite013-g5", "c"))
	log.Printf("%s", chr)
	chr.AddNode(NewNode("graphite014-g5", ""))
	log.Printf("%s", chr)
	chr.AddNode(NewNode("graphite019-g5", "foo"))
	log.Printf("%s", chr)

	log.Printf("Removing bad node...")
	chr.RemoveNode(NewNode("graphite019-g5", "foo"))
	log.Printf("%s", chr)

	log.Printf("GetNode: foo.bar.baz = %s", chr.GetNode("foo.bar.baz"))

	log.Printf("GetNodes():")
	for _, v := range chr.GetNodes("foo.bar.baz") {
		log.Printf("%s", v)
	}
}