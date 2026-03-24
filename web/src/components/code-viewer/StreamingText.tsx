"use client";

import React, { useEffect, useState } from "react";

export interface StreamingTextProps {
  text: string;
  speed?: number; // ms per word
  className?: string;
  onComplete?: () => void;
}

export function StreamingText({ text, speed = 30, className = "", onComplete }: StreamingTextProps) {
  const [displayed, setDisplayed] = useState("");
  const [isComplete, setIsComplete] = useState(false);

  useEffect(() => {
    if (!text) return;

    const words = text.split(" ");
    let index = 0;
    setDisplayed("");
    setIsComplete(false);

    const interval = setInterval(() => {
      index++;
      setDisplayed(words.slice(0, index).join(" "));
      if (index >= words.length) {
        clearInterval(interval);
        setIsComplete(true);
        onComplete?.();
      }
    }, speed);

    return () => clearInterval(interval);
  }, [text, speed, onComplete]);

  return (
    <div data-testid="streaming-text" data-complete={isComplete} className={className}>
      {displayed}
      {!isComplete && <span data-testid="streaming-cursor" className="opacity-50">|</span>}
    </div>
  );
}
