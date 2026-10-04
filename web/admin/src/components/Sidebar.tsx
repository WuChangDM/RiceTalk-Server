import type { ReactNode } from 'react'
import { Activity, Users, Settings, Hash, HardDrive, Package } from 'lucide-react'
import type { Tab } from '../types/admin'

const navItems: { key: Tab; label: string; icon: ReactNode }[] = [
  { key: 'server', label: '服务器设置', icon: <Settings size={16} aria-hidden="true" /> },
  { key: 'channels', label: '频道管理', icon: <Hash size={16} aria-hidden="true" /> },
  { key: 'users', label: '用户管理', icon: <Users size={16} aria-hidden="true" /> },
  { key: 'modules', label: '模块管理', icon: <Package size={16} aria-hidden="true" /> },
  { key: 'storage', label: '存储概览', icon: <HardDrive size={16} aria-hidden="true" /> },
  { key: 'runtime', label: '运行监控', icon: <Activity size={16} aria-hidden="true" /> },
]

interface SidebarProps {
  tab: Tab
  onTabChange: (tab: Tab) => void
}

export default function Sidebar({ tab, onTabChange }: SidebarProps) {
  return (
    <div className="admin-sidebar">
      <nav className="admin-nav">
        {navItems.map(item => (
          <div
            key={item.key}
            className={`admin-nav-item ${tab === item.key ? 'active' : ''}`}
            onClick={() => onTabChange(item.key)}
          >
            {item.icon} {item.label}
          </div>
        ))}
      </nav>
    </div>
  )
}
