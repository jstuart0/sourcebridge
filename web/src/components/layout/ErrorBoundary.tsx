"use client";

import { Component, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { Panel } from "@/components/ui/panel";

interface Props {
  children: ReactNode;
}

interface State {
  error: Error | null;
}

export class ErrorBoundary extends Component<Props, State> {
  constructor(props: Props) {
    super(props);
    this.state = { error: null };
  }

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  render() {
    if (this.state.error) {
      return (
        <div className="p-6">
          <Panel variant="elevated" className="mx-auto max-w-xl text-center">
            <h2 className="text-2xl font-semibold tracking-[-0.03em] text-[var(--text-primary)]">
              Something went wrong
            </h2>
            <p className="mt-3 text-sm leading-7 text-[var(--text-secondary)]">
              {this.state.error.message}
            </p>
            <div className="mt-6">
              <Button onClick={() => this.setState({ error: null })}>Try Again</Button>
            </div>
          </Panel>
        </div>
      );
    }
    return this.props.children;
  }
}
