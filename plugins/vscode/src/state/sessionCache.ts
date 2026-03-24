export class SessionCache<T> {
  private value?: T;
  private keyed = new Map<string, T>();

  get(): T | undefined {
    return this.value;
  }

  getKey(key: string): T | undefined {
    return this.keyed.get(key);
  }

  set(value: T): T {
    this.value = value;
    return value;
  }

  setKey(key: string, value: T): T {
    this.keyed.set(key, value);
    return value;
  }

  deleteKey(key: string): void {
    this.keyed.delete(key);
  }

  clear(): void {
    this.value = undefined;
    this.keyed.clear();
  }
}
