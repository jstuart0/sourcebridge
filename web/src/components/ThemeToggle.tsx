"use client";

import { useState, useEffect } from "react";
import { Moon, Sun } from "lucide-react";
import { useTheme } from "@/components/layout/ThemeProvider";
import { Button } from "@/components/ui/button";

export function ThemeToggle() {
  const { theme, setTheme } = useTheme();
  const [mounted, setMounted] = useState(false);

  useEffect(() => setMounted(true), []);

  // Render a placeholder during SSR + initial hydration to prevent icon flash.
  if (!mounted) {
    return <Button aria-label="Toggle theme" variant="ghost" size="icon" />;
  }

  return (
    <Button
      aria-label={theme === "dark" ? "Switch to light theme" : "Switch to dark theme"}
      variant="ghost"
      size="icon"
      onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
    >
      {/* Show target-state icon: Sun when dark (switch TO light), Moon when light (switch TO dark). */}
      {theme === "dark" ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
    </Button>
  );
}
