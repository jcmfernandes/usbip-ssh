//go:build e2e

package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
)

// qmpClient is a minimal QMP client: one synchronous command at a time,
// asynchronous event lines are skipped.
type qmpClient struct {
	conn net.Conn
	r    *bufio.Reader
}

func qmpConnect(path string) (*qmpClient, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return nil, err
	}
	c := &qmpClient{conn: conn, r: bufio.NewReader(conn)}
	m, err := c.readLine()
	if err == nil {
		if _, ok := m["QMP"]; !ok {
			err = fmt.Errorf("unexpected greeting")
		}
	}
	if err == nil {
		err = c.command("qmp_capabilities", nil)
	}
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("QMP handshake: %w", err)
	}
	return c, nil
}

func (c *qmpClient) readLine() (map[string]json.RawMessage, error) {
	line, err := c.r.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, fmt.Errorf("bad QMP line %q: %w", line, err)
	}
	return m, nil
}

// command sends an execute request and waits for its return, skipping
// interleaved event lines.
func (c *qmpClient) command(name string, args map[string]any) error {
	req := map[string]any{"execute": name}
	if args != nil {
		req["arguments"] = args
	}
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if _, err := c.conn.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("QMP %s: %w", name, err)
	}
	for {
		m, err := c.readLine()
		if err != nil {
			return fmt.Errorf("QMP %s: %w", name, err)
		}
		if _, ok := m["event"]; ok {
			continue
		}
		if e, ok := m["error"]; ok {
			return fmt.Errorf("QMP %s: %s", name, e)
		}
		if _, ok := m["return"]; ok {
			return nil
		}
	}
}

func (c *qmpClient) deviceAdd(driver, id string) error {
	return c.command("device_add", map[string]any{"driver": driver, "id": id, "bus": "xhci.0"})
}

func (c *qmpClient) deviceDel(id string) error {
	return c.command("device_del", map[string]any{"id": id})
}

func (c *qmpClient) quit() error {
	return c.command("quit", nil)
}

func (c *qmpClient) close() {
	c.conn.Close()
}
