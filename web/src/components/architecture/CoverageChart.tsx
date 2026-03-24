"use client";

import React from "react";
import {
  BarChart,
  Bar,
  XAxis,
  YAxis,
  Tooltip,
  ResponsiveContainer,
  Cell,
} from "recharts";

export interface CoverageDataPoint {
  name: string;
  category: string;
  covered: number;
  total: number;
}

export interface CoverageChartProps {
  data: CoverageDataPoint[];
  title?: string;
}

const categoryColors: Record<string, string> = {
  business: "#3b82f6",
  security: "#ef4444",
  data: "#22c55e",
  compliance: "#a855f7",
  performance: "#eab308",
  default: "#94a3b8",
};

function CoverageTooltip({
  active,
  payload,
  label,
}: {
  active?: boolean;
  payload?: Array<{ value?: number | string }>;
  label?: string;
}) {
  if (!active || !payload?.length) return null;
  return (
    <div className="rounded-[var(--radius-md)] border border-[var(--border-default,#334155)] bg-[var(--bg-elevated,#1e293b)] px-3 py-2 text-xs text-[var(--text-primary,#e2e8f0)] shadow-lg">
      <div className="font-medium">{label}</div>
      <div className="mt-1 text-[var(--text-secondary,#94a3b8)]">
        Coverage: {payload[0]?.value}%
      </div>
    </div>
  );
}

export function CoverageChart({ data, title = "Requirement Coverage" }: CoverageChartProps) {
  const chartData = data.map((d) => ({
    name: d.name,
    category: d.category,
    coverage: d.total > 0 ? Math.round((d.covered / d.total) * 100) : 0,
  }));

  return (
    <div data-testid="coverage-chart">
      {title && (
        <h3 className="mb-4 text-base font-semibold text-[var(--text-primary)]">{title}</h3>
      )}
      <ResponsiveContainer width="100%" height={300}>
        <BarChart data={chartData} margin={{ top: 5, right: 20, left: 0, bottom: 5 }}>
          <XAxis
            dataKey="name"
            tick={{ fill: "var(--text-secondary, #94a3b8)", fontSize: 11 }}
            tickLine={false}
            axisLine={{ stroke: "var(--border-default, #334155)" }}
          />
          <YAxis
            domain={[0, 100]}
            tick={{ fill: "var(--text-secondary, #94a3b8)", fontSize: 11 }}
            tickLine={false}
            axisLine={{ stroke: "var(--border-default, #334155)" }}
            tickFormatter={(v) => `${v}%`}
          />
          <Tooltip content={<CoverageTooltip />} />
          <Bar dataKey="coverage" radius={[4, 4, 0, 0]}>
            {chartData.map((entry, index) => (
              <Cell key={index} fill={categoryColors[entry.category] || categoryColors.default} />
            ))}
          </Bar>
        </BarChart>
      </ResponsiveContainer>
    </div>
  );
}
