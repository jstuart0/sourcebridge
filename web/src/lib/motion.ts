// Centralized motion configuration for SourceBridge
// Based on the UI/UX Design Specification

export const duration = {
  instant: 0,
  fast: 0.1,
  normal: 0.2,
  moderate: 0.3,
  slow: 0.5,
  slower: 0.8,
} as const;

export const spring = {
  snappy: { type: "spring" as const, stiffness: 500, damping: 30, mass: 0.5 },
  responsive: { type: "spring" as const, stiffness: 300, damping: 28, mass: 0.8 },
  gentle: { type: "spring" as const, stiffness: 200, damping: 24, mass: 1.0 },
  bouncy: { type: "spring" as const, stiffness: 400, damping: 15, mass: 0.8 },
  graph: { type: "spring" as const, stiffness: 120, damping: 20, mass: 1.2 },
} as const;

export const ease = {
  standard: [0.25, 0.1, 0.25, 1.0] as const,
  enter: [0.0, 0.0, 0.2, 1.0] as const,
  exit: [0.4, 0.0, 1.0, 1.0] as const,
  emphasis: [0.4, 0.0, 0.0, 1.0] as const,
} as const;

export const variants = {
  fadeIn: {
    initial: { opacity: 0 },
    animate: { opacity: 1, transition: { duration: duration.normal, ease: ease.enter } },
    exit: { opacity: 0, transition: { duration: duration.fast, ease: ease.exit } },
  },
  slideUp: {
    initial: { opacity: 0, y: 8 },
    animate: { opacity: 1, y: 0, transition: { duration: duration.normal, ease: ease.enter } },
    exit: { opacity: 0, y: 4, transition: { duration: duration.fast, ease: ease.exit } },
  },
  scaleIn: {
    initial: { opacity: 0, scale: 0.95 },
    animate: { opacity: 1, scale: 1, transition: spring.snappy },
    exit: { opacity: 0, scale: 0.97, transition: { duration: duration.fast } },
  },
  expand: {
    initial: { height: 0, opacity: 0 },
    animate: { height: "auto", opacity: 1, transition: spring.responsive },
    exit: { height: 0, opacity: 0, transition: { duration: duration.normal } },
  },
  staggerContainer: {
    animate: { transition: { staggerChildren: 0.03 } },
  },
  staggerItem: {
    initial: { opacity: 0, y: 4 },
    animate: { opacity: 1, y: 0 },
  },
  confidencePulse: {
    initial: { opacity: 0, scale: 0.9 },
    animate: { opacity: 1, scale: 1, transition: spring.bouncy },
  },
} as const;
