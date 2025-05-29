import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { Loading } from './Loading';

describe('Loading', () => {
  it('renders with default message', () => {
    render(<Loading />);
    expect(screen.getByText('Loading...')).toBeInTheDocument();
  });

  it('renders with custom message', () => {
    render(<Loading message="Please wait" />);
    expect(screen.getByText('Please wait')).toBeInTheDocument();
  });

  it('renders fullscreen when fullScreen prop is true', () => {
    const { container } = render(<Loading fullScreen />);
    const boxes = container.querySelectorAll('div');
    const fullScreenElement = Array.from(boxes).find(box => {
      const style = window.getComputedStyle(box);
      return style.position === 'fixed';
    });
    expect(fullScreenElement).toBeTruthy();
  });

  it('renders inline when fullScreen prop is false', () => {
    const { container } = render(<Loading fullScreen={false} />);
    const boxes = container.querySelectorAll('div');
    const fullScreenElement = Array.from(boxes).find(box => {
      const style = window.getComputedStyle(box);
      return style.position === 'fixed';
    });
    expect(fullScreenElement).toBeFalsy();
  });
});