import { query, queryAll } from '../dom/query';

export function initializeFeatureCarousel(root: HTMLElement): void {
  const first = query<HTMLElement>(root, '[data-feature-slide]');
  if (!first) {
    return;
  }
  const firstId = first.getAttribute('data-feature-slide');
  if (firstId) {
    setActiveFeatureSlide(root, firstId);
  }
}

export function shiftFeatureSlide(root: HTMLElement, delta: number): void {
  const slides = queryAll(root, '[data-feature-slide]');
  if (slides.length === 0) {
    return;
  }
  const currentIndex = slides.findIndex((slide) => slide.classList.contains('sdn-feature-slide--active'));
  const nextIndex = currentIndex === -1
    ? 0
    : (currentIndex + delta + slides.length) % slides.length;
  const nextId = slides[nextIndex]?.getAttribute('data-feature-slide');
  if (nextId) {
    setActiveFeatureSlide(root, nextId);
  }
}

export function setActiveFeatureSlide(root: HTMLElement, featureId: string): void {
  queryAll(root, '[data-feature-slide]').forEach((slide) => {
    const active = slide.getAttribute('data-feature-slide') === featureId;
    slide.classList.toggle('sdn-feature-slide--active', active);
    slide.setAttribute('aria-hidden', active ? 'false' : 'true');
  });
  queryAll(root, '[data-feature-target]').forEach((button) => {
    const active = button.getAttribute('data-feature-target') === featureId;
    button.classList.toggle('sdn-feature-carousel__indicator--active', active);
    button.setAttribute('aria-selected', active ? 'true' : 'false');
  });
}

export function bindFeatureCarousel(root: HTMLElement): void {
  query<HTMLButtonElement>(root, '[data-feature-prev]')?.addEventListener('click', () => {
    shiftFeatureSlide(root, -1);
  });
  query<HTMLButtonElement>(root, '[data-feature-next]')?.addEventListener('click', () => {
    shiftFeatureSlide(root, 1);
  });
  queryAll(root, '[data-feature-target]').forEach((item) => {
    if (!('addEventListener' in item)) {
      return;
    }
    item.addEventListener('click', () => {
      const target = item.getAttribute('data-feature-target');
      if (target) {
        setActiveFeatureSlide(root, target);
      }
    });
  });
  initializeFeatureCarousel(root);
}
