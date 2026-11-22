package main

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// One cache inside this gateway, not another reconciliation loop. Known dynamic
// ownership survives a transient file error; only an unexpired lease can serve.
type exportRouteCache struct {
	mu    sync.Mutex
	path  string
	owned map[string]exportRoute
}

var dynamicExports = &exportRouteCache{}

func exportUsable(route exportRoute, now time.Time) bool {
	if route.Revoked || route.Target == "" {
		return false
	}
	if !route.LeaseRequired {
		return true
	} // Explicit legacy static contract.
	expires, err := time.Parse(time.RFC3339Nano, route.ExpiresAt)
	return err == nil && now.Before(expires)
}

func (c *exportRouteCache) lookup(path string, static map[string]exportRoute, identity string, now time.Time) (exportRoute, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.path != path {
		c.path = path
		c.owned = map[string]exportRoute{}
	}
	data, err := os.ReadFile(path)
	var current []exportRoute
	valid := err == nil && json.Unmarshal(data, &current) == nil
	if valid {
		next := map[string]exportRoute{}
		for _, route := range current {
			if route.ServiceIdentity != "" {
				next[route.ServiceIdentity] = route
			}
		}
		for key, old := range c.owned {
			if _, ok := next[key]; !ok {
				next[key] = exportRoute{ServiceIdentity: key, LeaseRequired: true, Revoked: true}
			}
			// A previously leased identity cannot silently become an unlimited legacy route.
			if old.LeaseRequired && !next[key].LeaseRequired {
				next[key] = exportRoute{ServiceIdentity: key, LeaseRequired: true, Revoked: true}
			}
		}
		c.owned = next
	}
	if route, known := c.owned[identity]; known {
		if !valid && !route.LeaseRequired {
			return exportRoute{}, false
		}
		return route, exportUsable(route, now)
	}
	route, ok := static[identity]
	return route, ok && exportUsable(route, now)
}
