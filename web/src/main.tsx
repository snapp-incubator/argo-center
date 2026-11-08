import '@mantine/core/styles.css';
import '@fontsource-variable/inter';
import '@fontsource-variable/jetbrains-mono';
import './global.css';

import { lazy, StrictMode, Suspense } from 'react';
import { createRoot } from 'react-dom/client';
import { Loader, MantineProvider } from '@mantine/core';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { BrowserRouter, Route, Routes } from 'react-router';
import { Layout } from './components/Layout';
import { Overview } from './pages/Overview';
import { NotFound } from './pages/NotFound';
import { theme } from './theme';

// The overview is the landing page; the others load on first visit.
const AppDetail = lazy(() => import('./pages/AppDetail').then((m) => ({ default: m.AppDetail })));
const Audit = lazy(() => import('./pages/Audit').then((m) => ({ default: m.Audit })));
const page = (el: React.ReactNode) => <Suspense fallback={<Loader m="xl" />}>{el}</Suspense>;

// Data is pushed via /api/events, so no polling or focus refetching is needed.
const queryClient = new QueryClient({
  defaultOptions: { queries: { staleTime: Infinity, refetchOnWindowFocus: false } },
});

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <MantineProvider theme={theme} defaultColorScheme="auto">
      <QueryClientProvider client={queryClient}>
        <BrowserRouter>
          <Routes>
            <Route element={<Layout />}>
              <Route index element={<Overview />} />
              <Route path="apps/:name" element={page(<AppDetail />)} />
              <Route path="audit" element={page(<Audit />)} />
              <Route path="*" element={<NotFound />} />
            </Route>
          </Routes>
        </BrowserRouter>
      </QueryClientProvider>
    </MantineProvider>
  </StrictMode>,
);
