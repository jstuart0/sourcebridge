import { createItem, getItem, listItems, deleteItem, handleRequest } from "../src/api";

describe("API handlers", () => {
  test("REQ-003: createItem creates valid item", () => {
    const item = createItem("Test Item", "A test item");
    expect(item.id).toBeTruthy();
    expect(item.name).toBe("Test Item");
  });

  test("REQ-004: createItem rejects empty name", () => {
    expect(() => createItem("", "desc")).toThrow("Name is required");
  });

  test("REQ-003: getItem returns created item", () => {
    const created = createItem("Find Me", "desc");
    const found = getItem(created.id);
    expect(found).toBeDefined();
    expect(found?.name).toBe("Find Me");
  });

  test("REQ-006: listItems supports search", () => {
    createItem("Alpha", "first");
    createItem("Beta", "second");
    const result = listItems({ search: "Alpha" });
    expect(result.items.some((i) => i.name === "Alpha")).toBe(true);
  });

  test("REQ-004: handleRequest routes correctly", () => {
    const response = handleRequest({
      method: "POST",
      path: "/items",
      body: { name: "New Item", description: "desc" },
      headers: {},
    });
    expect(response.status).toBe(201);
  });
});
