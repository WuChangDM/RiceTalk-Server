import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import ModulesPanel from '../ModulesPanel'
import type { ModuleInfo } from '../../types/admin'
import * as apiCore from '../../../../shared/api-core'

vi.mock('../../../../shared/api-core', () => ({
  toggleModule: vi.fn(),
  apiPost: vi.fn(),
  getAdminModules: vi.fn(),
  getAdminLogs: vi.fn(),
}))

describe('ModulesPanel', () => {
  const baseModules: ModuleInfo[] = [
    { name: 'voice', enabled: true, status: 'running', description: 'Voice module', cpu: 10, memory: 64 },
    { name: 'bots', enabled: false, status: 'disabled', description: 'Bot module', cpu: 0, memory: 0 },
  ]

  it('renders module list with statuses', () => {
    render(
      <ModulesPanel
        modules={baseModules}
        setModules={vi.fn()}
        auditLogs={[]}
        setAuditLogs={vi.fn()}
        refreshAuditLogsNow={vi.fn()}
        auditLogsRefreshing={false}
        moduleGracePeriod={null}
        setModuleGracePeriod={vi.fn()}
      />
    )
    expect(screen.getByText('模块管理')).toBeInTheDocument()
    expect(screen.getByText('Voice module')).toBeInTheDocument()
    expect(screen.getByText('Bot module')).toBeInTheDocument()
    expect(screen.getByText((content) => content.includes('运行中'))).toBeInTheDocument()
    expect(screen.getByText((content) => content.includes('已禁用'))).toBeInTheDocument()
  })

  it('toggles a running module off and refreshes the list', async () => {
    vi.mocked(apiCore.toggleModule).mockResolvedValue({ code: 'OK' } as any)
    vi.mocked(apiCore.getAdminModules).mockResolvedValue({
      code: 'OK',
      data: { items: [{ name: 'voice', enabled: false, status: 'disabled', description: 'Voice module' }] },
    } as any)

    const setModules = vi.fn()
    const setModuleGracePeriod = vi.fn()
    render(
      <ModulesPanel
        modules={baseModules}
        setModules={setModules}
        auditLogs={[]}
        setAuditLogs={vi.fn()}
        refreshAuditLogsNow={vi.fn()}
        auditLogsRefreshing={false}
        moduleGracePeriod={null}
        setModuleGracePeriod={setModuleGracePeriod}
      />
    )

    const disableBtn = screen.getAllByRole('button', { name: /禁用/i })[0]
    fireEvent.click(disableBtn)

    await waitFor(() => {
      expect(apiCore.toggleModule).toHaveBeenCalledWith('voice', 'disable')
      expect(setModuleGracePeriod).toHaveBeenCalledWith({ moduleName: 'voice', remainingSeconds: 300 })
      expect(apiCore.getAdminModules).toHaveBeenCalled()
    })
  })

  it('toggles a disabled module on', async () => {
    vi.mocked(apiCore.toggleModule).mockResolvedValue({ code: 'OK' } as any)
    vi.mocked(apiCore.getAdminModules).mockResolvedValue({
      code: 'OK',
      data: { items: [{ name: 'bots', enabled: true, status: 'running', description: 'Bot module' }] },
    } as any)

    const setModules = vi.fn()
    render(
      <ModulesPanel
        modules={baseModules}
        setModules={setModules}
        auditLogs={[]}
        setAuditLogs={vi.fn()}
        refreshAuditLogsNow={vi.fn()}
        auditLogsRefreshing={false}
        moduleGracePeriod={null}
        setModuleGracePeriod={vi.fn()}
      />
    )

    const enableBtn = screen.getByRole('button', { name: /启用/i })
    fireEvent.click(enableBtn)

    await waitFor(() => {
      expect(apiCore.toggleModule).toHaveBeenCalledWith('bots', 'enable')
    })
  })
})
