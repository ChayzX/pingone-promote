package pingone

// RunInventory exposes Inventory/InventoryVersion for the scratch cross-check.
import "context"

func RunInventory(c *Client, env, pin string) (Inventory, error) {
	if pin != "" {
		return c.InventoryVersion(context.Background(), env, pin)
	}
	return c.Inventory(context.Background(), env)
}
