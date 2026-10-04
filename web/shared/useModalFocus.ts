import { useEffect, useRef, RefObject } from 'react'

// D-3: Modal 焦点陷阱与可访问性 hook
// 提供：Escape 关闭、Tab/Shift+Tab 焦点循环、打开时聚焦首元素、关闭后焦点返回触发元素
//
// 用法：
//   const modalRef = useRef<HTMLDivElement>(null)
//   const triggerRef = useRef<HTMLButtonElement>(null)
//   useModalFocus(isOpen, onClose, modalRef, triggerRef)
//
// 在 modal 容器上添加 ref={modalRef} role="dialog" aria-modal="true"

const FOCUSABLE_SELECTOR = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(', ')

export function useModalFocus(
  open: boolean,
  onClose: () => void,
  modalRef: RefObject<HTMLElement | null>,
  triggerRef?: RefObject<HTMLElement | null> | null
) {
  // 记录打开前的焦点元素，用于关闭后恢复
  const previousFocusRef = useRef<HTMLElement | null>(null)

  useEffect(() => {
    if (!open || !modalRef.current) return

    // 记录当前焦点（触发元素）
    previousFocusRef.current = (document.activeElement as HTMLElement) || null

    const modal = modalRef.current

    // 聚焦 modal 内首个可交互元素（preventScroll 避免页面被拉回聚焦元素位置）
    const focusables = modal.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR)
    if (focusables.length > 0) {
      // 优先聚焦关闭按钮或首个 input
      const firstInput = Array.from(focusables).find(el => el.tagName === 'INPUT' || el.tagName === 'TEXTAREA')
      ;(firstInput || focusables[0]).focus({ preventScroll: true })
    } else {
      modal.focus({ preventScroll: true })
    }

    const handleKeyDown = (e: KeyboardEvent) => {
      // Escape 关闭
      if (e.key === 'Escape') {
        e.preventDefault()
        onClose()
        return
      }

      // Tab / Shift+Tab 焦点循环
      if (e.key === 'Tab') {
        const currentFocusables = modal.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR)
        if (currentFocusables.length === 0) {
          e.preventDefault()
          modal.focus()
          return
        }

        const first = currentFocusables[0]
        const last = currentFocusables[currentFocusables.length - 1]

        if (e.shiftKey) {
          // Shift+Tab：从首个元素跳到最后一个
          if (document.activeElement === first || !modal.contains(document.activeElement)) {
            e.preventDefault()
            last.focus()
          }
        } else {
          // Tab：从最后一个元素跳到第一个
          if (document.activeElement === last || !modal.contains(document.activeElement)) {
            e.preventDefault()
            first.focus()
          }
        }
      }
    }

    modal.addEventListener('keydown', handleKeyDown)

    return () => {
      modal.removeEventListener('keydown', handleKeyDown)
      // 关闭后焦点返回触发元素
      const restoreFocus = () => {
        const target = triggerRef?.current || previousFocusRef.current
        if (target && document.contains(target)) {
          target.focus({ preventScroll: true })
        }
      }
      // 延迟一帧恢复焦点，确保 DOM 已更新
      requestAnimationFrame(restoreFocus)
    }
  }, [open, onClose, modalRef, triggerRef])
}
