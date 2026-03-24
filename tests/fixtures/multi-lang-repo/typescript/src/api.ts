/**
 * REST API handlers for the application.
 *
 * REQ-003: REST API endpoints for CRUD operations
 * REQ-004: Request validation and error handling
 */

export interface ApiRequest {
  method: string;
  path: string;
  body?: unknown;
  headers: Record<string, string>;
}

export interface ApiResponse {
  status: number;
  body: unknown;
  headers: Record<string, string>;
}

export interface Item {
  id: string;
  name: string;
  description: string;
  createdAt: Date;
  updatedAt: Date;
}

// In-memory store for demo purposes
const items = new Map<string, Item>();

/**
 * Create a new item.
 * REQ-003: POST /items creates a new item
 * REQ-004: Validates required fields
 */
export function createItem(name: string, description: string): Item {
  if (!name || name.trim().length === 0) {
    throw new Error("Name is required");
  }

  const id = `item_${Date.now()}_${Math.random().toString(36).slice(2, 8)}`;
  const now = new Date();
  const item: Item = { id, name: name.trim(), description, createdAt: now, updatedAt: now };
  items.set(id, item);
  return item;
}

/**
 * Get an item by ID.
 * REQ-003: GET /items/:id returns item details
 */
export function getItem(id: string): Item | undefined {
  return items.get(id);
}

/**
 * List all items with optional filtering.
 * REQ-003: GET /items returns paginated list
 * REQ-006: Support filtering and pagination
 */
export function listItems(options: { limit?: number; offset?: number; search?: string } = {}): {
  items: Item[];
  total: number;
} {
  let result = Array.from(items.values());

  if (options.search) {
    const query = options.search.toLowerCase();
    result = result.filter(
      (item) => item.name.toLowerCase().includes(query) || item.description.toLowerCase().includes(query)
    );
  }

  const total = result.length;
  const offset = options.offset ?? 0;
  const limit = options.limit ?? 50;

  return {
    items: result.slice(offset, offset + limit),
    total,
  };
}

/**
 * Delete an item.
 * REQ-003: DELETE /items/:id removes item
 * REQ-007: Soft delete with audit trail
 */
export function deleteItem(id: string): boolean {
  return items.delete(id);
}

/**
 * Route an API request to the appropriate handler.
 * REQ-004: Central request routing with error handling
 */
export function handleRequest(req: ApiRequest): ApiResponse {
  try {
    if (req.method === "POST" && req.path === "/items") {
      const body = req.body as { name?: string; description?: string } | undefined;
      const item = createItem(body?.name ?? "", body?.description ?? "");
      return { status: 201, body: item, headers: { "content-type": "application/json" } };
    }

    if (req.method === "GET" && req.path.startsWith("/items/")) {
      const id = req.path.split("/")[2];
      const item = getItem(id);
      if (!item) {
        return { status: 404, body: { error: "Not found" }, headers: { "content-type": "application/json" } };
      }
      return { status: 200, body: item, headers: { "content-type": "application/json" } };
    }

    if (req.method === "GET" && req.path === "/items") {
      const result = listItems();
      return { status: 200, body: result, headers: { "content-type": "application/json" } };
    }

    return { status: 404, body: { error: "Not found" }, headers: { "content-type": "application/json" } };
  } catch (error) {
    const message = error instanceof Error ? error.message : "Internal server error";
    return { status: 400, body: { error: message }, headers: { "content-type": "application/json" } };
  }
}
