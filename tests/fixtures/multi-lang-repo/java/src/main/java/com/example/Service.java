package com.example;

import java.util.*;
import java.time.Instant;

/**
 * Core service for data processing.
 *
 * REQ-014: Data processing pipeline
 * REQ-015: Data validation before processing
 */
public class Service {

    private final Map<String, Record> store = new HashMap<>();

    /**
     * A data record.
     */
    public static class Record {
        public String id;
        public String data;
        public Instant createdAt;
        public boolean processed;

        public Record(String id, String data) {
            this.id = id;
            this.data = data;
            this.createdAt = Instant.now();
            this.processed = false;
        }
    }

    /**
     * Ingest a new data record.
     * REQ-014: Accept and store data for processing
     * REQ-015: Validate data before storage
     */
    public Record ingest(String id, String data) {
        if (id == null || id.isEmpty()) {
            throw new IllegalArgumentException("ID is required");
        }
        if (data == null || data.isEmpty()) {
            throw new IllegalArgumentException("Data is required");
        }
        Record record = new Record(id, data);
        store.put(id, record);
        return record;
    }

    /**
     * Process a stored record.
     * REQ-014: Transform data according to rules
     */
    public Record process(String id) {
        Record record = store.get(id);
        if (record == null) {
            throw new NoSuchElementException("Record not found: " + id);
        }
        // Transform data
        record.data = record.data.toUpperCase();
        record.processed = true;
        return record;
    }

    /**
     * Retrieve a record by ID.
     */
    public Optional<Record> get(String id) {
        return Optional.ofNullable(store.get(id));
    }

    /**
     * List all records.
     * REQ-006: Support listing with pagination
     */
    public List<Record> list(int offset, int limit) {
        List<Record> all = new ArrayList<>(store.values());
        int end = Math.min(offset + limit, all.size());
        if (offset >= all.size()) return Collections.emptyList();
        return all.subList(offset, end);
    }
}
