package cache

/*
Package cache is a local cache tool.

AUTHOR:
	kevin.gao@ucloud.cn

LastChange:
	2017-07-25 17:02:40

Version:
	0.0.1

Type Cache:
	func New(defaultExpiration, cleanupInterval time.Duration) *Cache
	func NewFrom(defaultExpiration, cleanupInterval time.Duration, items map[string]Item) *Cache
	func (c Cache) Add(k string, x interface{}, d time.Duration) error
	func (c Cache) Get(k string) (interface{}, bool)
	func (c Cache) GetWithExpiration(k string) (interface{}, time.Time, bool)
	func (c Cache) Set(k string, x interface{}, d time.Duration)
	func (c Cache) SetDefault(k string, x interface{})
	func (c Cache) Delete(k string)
	func (c Cache) DeleteExpired()
	func (c Cache) Flush()
	func (c Cache) ItemCount() int
	func (c Cache) Items() map[string]Item
	func (c Cache) Replace(k string, x interface{}, d time.Duration) error
	func (c Cache) OnEvicted(f func(string, interface{}))
*/

import (
	"fmt"
	"runtime"
	"sync"
	"time"
)

// Item 须要 cache 的对象
type Item struct {
	Object     interface{}
	Expiration int64
}

// Expired 判断过期，如果 Expiration 为 0，则永不过期，暂时用不到
func (item Item) Expired() bool {
	if item.Expiration == 0 {
		return false
	}
	return time.Now().UnixNano() > item.Expiration
}

// 使用time.Duration 类型（int64, min: -1 << 63 max: 1<<63 - 1）
const (
	NoExpiration      time.Duration = -1
	DefaultExpiration time.Duration = 0
)

// Cache 如果不理解可以看New()
type Cache struct {
	*cache
}

type cache struct {
	defaultExpiration time.Duration
	items             map[string]Item
	mu                sync.RWMutex              // 读写锁
	onEvicted         func(string, interface{}) // 这里是为删除的时候加一个钩子方法，即删除前调用这个方法（可选）
	janitor           *janitor
}

func (c *cache) Objects() map[string]interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m := make(map[string]interface{}, len(c.items))
	now := time.Now().UnixNano()
	for k, v := range c.items {
		if v.Expiration > 0 {
			if now > v.Expiration {
				continue
			}
		}
		m[k] = v.Object
	}
	return m
}

func (c *cache) Items() map[string]Item {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m := make(map[string]Item, len(c.items))
	now := time.Now().UnixNano()
	for k, v := range c.items {
		if v.Expiration > 0 {
			if now > v.Expiration {
				continue
			}
		}
		m[k] = v
	}
	return m
}

// 创建一个 cache，并给一个de(default duration)
func newCache(de time.Duration, m map[string]Item) *cache {
	if de == 0 {
		de = -1
	}
	c := &cache{
		defaultExpiration: de,
		items:             m,
	}
	return c
}

func newCacheWithJanitor(de time.Duration, ci time.Duration, m map[string]Item) *Cache {
	c := newCache(de, m)
	C := &Cache{c}
	if ci > 0 {
		runJanitor(c, ci)
		// 在对象 C 被从内存移除前对 C 执行 stopJanitor 方法
		runtime.SetFinalizer(C, stopJanitor)
	}
	return C
}

// New 返回一个新的 cache -- 包含一个默认的超时时间，和清理间隔
// 如果超时时间小于1（或者不到期），cache 中的元素永远不会过期，必须手工删除。
// 如果清理间隔小于1，在调用 DeleteExpired 方法之前将不会被删除
func New(defaultExpiration, cleanupInterval time.Duration) *Cache {
	items := make(map[string]Item)
	return newCacheWithJanitor(defaultExpiration, cleanupInterval, items)
}

// NewFrom 比 New 额外接受一个 items map
func NewFrom(defaultExpiration, cleanupInterval time.Duration, items map[string]Item) *Cache {
	return newCacheWithJanitor(defaultExpiration, cleanupInterval, items)
}

func (c *cache) Set(k string, x interface{}, d time.Duration) {
	var e int64
	if d == DefaultExpiration {
		d = c.defaultExpiration
	}
	if d > 0 {
		e = time.Now().Add(d).UnixNano()
	}
	c.mu.Lock() // 写锁定

	// 据说defer会多耗费~200纳秒
	defer c.mu.Unlock() // 写解锁

	c.items[k] = Item{
		Object:     x,
		Expiration: e,
	}
}

func (c *cache) set(k string, x interface{}, d time.Duration) {
	var e int64
	if d == DefaultExpiration {
		d = c.defaultExpiration
	}
	if d > 0 {
		e = time.Now().UnixNano()
	}
	c.items[k] = Item{
		Object:     x,
		Expiration: e,
	}
}

// 添加一个 item 到 cache 中，替换任何已存在的 item，使用默认的超时设置:
// const DefaultExpiration Duration = 0
func (c *cache) SetDefault(k string, x interface{}) {
	c.Set(k, x, DefaultExpiration)
}

// 添加一个 cache 中本来就没有的或者存在但是过期了的 item
func (c *cache) Add(k string, x interface{}, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	_, found := c.get(k)
	if found {
		return fmt.Errorf("Item %s already exists", k)
	}
	// 小写的 set 没有写锁定
	c.set(k, x, d)
	return nil
}

// 就是替换 cache 中已存在的 item，没有就报错
func (c *cache) Replace(k string, x interface{}, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	_, found := c.get(k)
	if !found {
		return fmt.Errorf("Item %s doesn't exist", k)
	}
	c.set(k, x, d)
	return nil
}

func (c *cache) Get(k string) (interface{}, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	item, found := c.items[k]
	if !found {
		return nil, false
	}
	if item.Expiration > 0 {
		if time.Now().UnixNano() > item.Expiration {
			return nil, false
		}
	}
	return item.Object, true
}

// 不仅返回 item 的值，还返回过期时间
func (c *cache) GetWithExpiration(k string) (interface{}, time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	item, found := c.items[k]
	if !found {
		return nil, time.Time{}, false
	}

	if item.Expiration > 0 {
		if time.Now().UnixNano() > item.Expiration {
			return nil, time.Time{}, false
		}

		return item.Object, time.Unix(0, item.Expiration), true
	}

	// 存在且永不超时的 item
	return item.Object, time.Time{}, true
}

func (c *cache) get(k string) (interface{}, bool) {
	item, found := c.items[k]
	if !found {
		return nil, false
	}

	if item.Expiration > 0 {
		if time.Now().UnixNano() > item.Expiration {
			return nil, false
		}
	}

	return item.Object, true
}

// 从 cache 中删除 item，如果 cache 中没有，什么也不做
func (c *cache) Delete(k string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 逐出
	v, evicted := c.delete(k)
	if evicted {
		c.onEvicted(k, v)
	}
}

func (c *cache) delete(k string) (interface{}, bool) {
	if c.onEvicted != nil {
		if v, found := c.items[k]; found {
			delete(c.items, k)
			return v.Object, true
		}
	}
	delete(c.items, k)
	return nil, false
}

type keyAndValue struct {
	key   string
	value interface{}
}

// 删除超时的 item，并执行指定的钩子函数（可选）
func (c *cache) DeleteExpired() {
	var evictedItems []keyAndValue
	now := time.Now().UnixNano()
	c.mu.Lock()

	// 将过期的item删除的同时，保留一份给删除钩子来执行
	for k, v := range c.items {
		if v.Expiration > 0 && now > v.Expiration {
			ov, evicted := c.delete(k)
			if evicted {
				evictedItems = append(evictedItems, keyAndValue{k, ov})
			}
		}
	}
	c.mu.Unlock()

	// 删除钩子方法对已删除的 item 做处理
	for _, v := range evictedItems {
		c.onEvicted(v.key, v.value)
	}
}

// 为 cache 对象 添加一个删除时的钩子方法
func (c *cache) OnEvicted(f func(string, interface{})) {
	c.mu.Lock()
	c.onEvicted = f
	c.mu.Unlock()
}

func (c *cache) ItemCount() int {
	c.mu.RLock()
	n := len(c.items)
	c.mu.RUnlock()
	return n
}

func (c *cache) Flush() {
	c.mu.Lock()
	c.items = map[string]Item{}
	c.mu.Unlock()
}

type janitor struct {
	Interval time.Duration
	stop     chan bool
}

func (j *janitor) Run(c *cache) {
	// 创建一个新 ticker 每隔 j.Interval 时间会发送时间到新 ticker 的 channel
	ticker := time.NewTicker(j.Interval)
	for {
		select {
		case <-ticker.C: // 如果没有时间通过 channel 过来，那么说明一直没有超时
			c.DeleteExpired()
		case <-j.stop:
			ticker.Stop()
			return
		}
	}
}

// 停止 janitor 的『等待超时然后删除』的操作
func stopJanitor(c *Cache) {
	c.janitor.stop <- true
}

// 重新给超时时间并开启 janitor 的『等待超时然后删除』操作
func runJanitor(c *cache, ci time.Duration) {
	j := &janitor{
		Interval: ci,
		stop:     make(chan bool),
	}
	c.janitor = j
	go j.Run(c)
}
