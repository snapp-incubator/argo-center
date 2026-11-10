import { Component, type ErrorInfo, type ReactNode } from 'react';
import { Alert, Button, Code, Group, Stack, Text } from '@mantine/core';

/**
 * Shows an error message when a page crashes while rendering. Without it, React removes the whole
 * app and leaves an empty screen. Give it a `key` that changes with the page, so going to another
 * page clears the error.
 */
export class ErrorBoundary extends Component<{ children: ReactNode }, { error?: Error }> {
  state: { error?: Error } = {};

  static getDerivedStateFromError(error: Error) {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('Page crashed:', error, info.componentStack);
  }

  render() {
    const { error } = this.state;
    if (!error) return this.props.children;
    return (
      <Alert color="red" title="This page crashed" mt="md">
        <Stack gap="sm" align="flex-start">
          <Text size="sm">This is a bug in argo-center. The rest of the app still works.</Text>
          <Code block style={{ whiteSpace: 'pre-wrap', alignSelf: 'stretch' }}>
            {error.message || String(error)}
          </Code>
          <Group gap="xs">
            <Button size="xs" variant="default" onClick={() => this.setState({ error: undefined })}>
              Try again
            </Button>
            <Button size="xs" variant="default" onClick={() => window.location.reload()}>
              Reload page
            </Button>
          </Group>
        </Stack>
      </Alert>
    );
  }
}
