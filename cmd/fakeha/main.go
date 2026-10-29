// Command fakeha runs a fake Home Assistant WebSocket API with demo data, for
// local development of hafleet without a real HA instance.
//
//	go run ./cmd/fakeha            # prints the URL and token to use
//	go run ./cmd/fakeha -variant b # a second instance with other versions and add-ons (drift demo)
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/hass/hasstest"
)

func main() {
	token := flag.String("token", "dev-token", "access token to accept")
	variant := flag.String("variant", "a", "demo data variant: a or b (differs in versions and add-ons)")
	flag.Parse()

	s := hasstest.New(*token)
	defer s.Close()

	osVersion := "16.2"
	addons := []any{
		addon("core_mosquitto", "Mosquitto broker", "6.4.0", "6.5.1"),
		addon("a0d7b954_vscode", "Studio Code Server", "5.19.0", "5.19.0"),
		addon("core_samba", "Samba share", "12.3.2", "12.3.2"),
	}
	if *variant == "b" {
		s.Version = "2026.9.3"
		osVersion = "16.1"
		addons = []any{
			addon("core_mosquitto", "Mosquitto broker", "6.5.1", "6.5.1"),
			addon("core_zwave_js", "Z-Wave JS", "0.12.0", "0.12.0"),
			addon("core_samba", "Samba share", "12.3.2", "12.3.2"),
		}
	}

	var mu sync.Mutex
	updates := map[string]map[string]any{
		"update.home_assistant_core_update": update("Home Assistant Core", "2026.10.1", "2026.10.2", 1|8|16),
		"update.mosquitto_broker_update":    update("Mosquitto broker", "6.4.0", "6.5.1", 1|8|16),
		"update.hacs_mushroom_update":       update("Mushroom", "4.1.0", "4.2.0", 1|16),
	}
	// Health demo data: two low batteries and three Zigbee lights unavailable for two hours.
	old := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	other := []any{
		map[string]any{"entity_id": "sensor.front_door_battery", "state": "8", "last_changed": old,
			"attributes": map[string]any{"device_class": "battery", "unit_of_measurement": "%"}},
		map[string]any{"entity_id": "sensor.garage_motion_battery", "state": "12", "last_changed": old,
			"attributes": map[string]any{"device_class": "battery", "unit_of_measurement": "%"}},
	}
	var registry []any
	for _, id := range []string{"light.porch", "light.shed", "light.garden"} {
		other = append(other, map[string]any{"entity_id": id, "state": "unavailable", "last_changed": old, "attributes": map[string]any{}})
		registry = append(registry, map[string]any{"entity_id": id, "platform": "zha", "disabled_by": nil})
	}
	s.Result("config/entity_registry/list", registry)
	s.Handle("system_log/list", func(map[string]any) (any, string) {
		now := float64(time.Now().Unix())
		return []any{map[string]any{"name": "homeassistant.components.zha.core", "message": []string{"Device 0x4f2a did not respond"},
			"level": "ERROR", "timestamp": now - 120, "first_occurred": now - 1800, "count": 12}}, ""
	})
	diskFree := 21.0
	if *variant == "b" {
		diskFree = 2.6 // low disk on the second instance
	}

	states := func() []any {
		mu.Lock()
		defer mu.Unlock()
		out := append([]any{}, other...)
		for id, u := range updates {
			out = append(out, map[string]any{"entity_id": id, "state": u["_state"], "attributes": u})
		}
		return out
	}

	s.Result("get_config", map[string]any{
		"version": s.Version, "location_name": "Fake Home", "components": []string{"hassio", "backup", "mqtt", "hue", "zha"},
	})
	s.Result("auth/current_user", map[string]any{"id": "fakeha-dev-user", "is_admin": true, "name": "dev"})
	s.Handle("get_states", func(map[string]any) (any, string) { return states(), "" })
	s.Result("repairs/list_issues", map[string]any{"issues": []any{
		map[string]any{"domain": "hacs", "issue_id": "restart_required_mushroom", "severity": "warning",
			"translation_key": "restart_required", "translation_placeholders": map[string]string{"name": "Mushroom"},
			"ignored": false, "dismissed_version": nil, "is_fixable": true},
	}})
	s.Result("frontend/get_translations", map[string]any{"resources": map[string]string{
		"component.hacs.issues.restart_required.title": "Restart required for {name}",
	}})
	s.Result("config_entries/get", []any{
		map[string]any{"entry_id": "e1", "domain": "mqtt", "title": "MQTT", "state": "setup_retry", "reason": "Connection refused"},
	})
	s.Result("config_entries/flow/progress", []any{})
	s.Result("backup/info", map[string]any{"backups": []any{
		map[string]any{"backup_id": "b1", "name": "Old", "date": time.Now().Add(-10 * 24 * time.Hour).UTC().Format(time.RFC3339)},
	}})
	s.Handle("supervisor/api", func(cmd map[string]any) (any, string) {
		switch cmd["endpoint"] {
		case "/resolution/info":
			return map[string]any{"unsupported": []string{}, "unhealthy": []string{},
				"suggestions": []any{map[string]any{"uuid": "s1", "type": "execute_reboot", "context": "system"}}, "issues": []any{}}, ""
		case "/core/check":
			return map[string]any{}, ""
		case "/info":
			return map[string]any{"supervisor": "2026.10.0", "hassos": osVersion, "machine": "generic-x86-64",
				"hostname": "fake-ha"}, ""
		case "/host/info":
			return map[string]any{"disk_total": 28.6, "disk_used": 28.6 - diskFree, "disk_free": diskFree, "hostname": "fake-ha"}, ""
		case "/addons":
			return map[string]any{"addons": addons}, ""
		}
		return nil, "not_found"
	})
	s.Handle("update/release_notes", func(cmd map[string]any) (any, string) {
		if cmd["entity_id"] == "update.home_assistant_core_update" {
			return coreNotes, ""
		}
		return fmt.Sprintf("## %v\n\n- Fixed things\n- Improved other things", cmd["entity_id"]), ""
	})
	s.Result("backup/generate_with_automatic_settings", map[string]any{})
	s.Handle("call_service", func(cmd map[string]any) (any, string) {
		data, _ := cmd["service_data"].(map[string]any)
		id, _ := data["entity_id"].(string)
		if cmd["domain"] == "update" {
			mu.Lock()
			u, ok := updates[id]
			if ok {
				u["_state"] = "off"
				u["installed_version"] = u["latest_version"]
			}
			mu.Unlock()
			if ok {
				log.Printf("%s %s", cmd["service"], id)
				s.FireEvent(map[string]any{"event_type": "state_changed", "data": map[string]any{
					"entity_id": id, "new_state": map[string]any{"entity_id": id, "state": "off", "attributes": u}}})
			}
		} else {
			log.Printf("service %s.%s", cmd["domain"], cmd["service"])
		}
		return map[string]any{}, ""
	})

	s.Handle("fire_event", func(cmd map[string]any) (any, string) {
		data, _ := json.Marshal(cmd["event_data"])
		log.Printf("event %s: %s", cmd["event_type"], data)
		return map[string]any{}, ""
	})

	fmt.Printf("fake Home Assistant running\n  URL:   %s\n  token: %s\n(enable 'Allow insecure plain HTTP' when adding it)\n", s.URL, *token)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
}

func addon(slug, name, version, latest string) map[string]any {
	return map[string]any{"slug": slug, "name": name, "version": version, "version_latest": latest,
		"update_available": version != latest, "state": "started"}
}

func update(title, installed, latest string, features int) map[string]any {
	return map[string]any{
		"_state": "on", "title": title, "installed_version": installed, "latest_version": latest,
		"supported_features": features, "in_progress": false,
		"release_url": "https://www.home-assistant.io/blog/",
	}
}

const coreNotes = `# Home Assistant 2026.10.2

## Backward-incompatible changes

### MQTT

The deprecated ` + "`state_topic`" + ` option for MQTT light entities configured in YAML has been removed.
Migrate to ` + "`command_topic`" + `/` + "`state_value_template`" + ` before updating, or affected lights become unavailable.

### Nest

The legacy Works with Nest API is no longer supported.

### Philips Hue

No breaking changes; bridges running firmware older than 1.50 now show a repair issue.

## All changes

- Bump zha dependency
- Fix MQTT discovery for sensors
`
