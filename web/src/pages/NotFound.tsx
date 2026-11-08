import { Anchor, Stack, Text, Title } from '@mantine/core';
import { Link } from 'react-router';
import { usePageTitle } from '../usePageTitle';

export function NotFound() {
  usePageTitle('Not found');
  return (
    <Stack gap="xs" mt="xl">
      <Title order={2}>Page not found</Title>
      <Text c="dimmed">
        Nothing lives at this address.{' '}
        <Anchor component={Link} to="/">
          Back to applications
        </Anchor>
      </Text>
    </Stack>
  );
}
