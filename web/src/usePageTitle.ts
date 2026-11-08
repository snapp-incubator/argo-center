import { useEffect } from 'react';

/** Sets the browser tab title: "<title> · argo-center", or just "argo-center". */
export function usePageTitle(title?: string) {
  useEffect(() => {
    document.title = title ? `${title} · argo-center` : 'argo-center';
  }, [title]);
}
