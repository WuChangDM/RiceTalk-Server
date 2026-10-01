const errorMap: Record<string, string> = {
  'invalid credentials': '邮箱或密码错误',
  'user not found': '用户不存在',
  'unauthorized': '未授权，请重新登录',
  'forbidden': '权限不足',
  'network error': '网络连接失败，请检查网络',
  'server error': '服务器错误，请稍后重试',
};

export function translateError(err: string): string {
  const key = err.toLowerCase().trim();
  return errorMap[key] || err;
}
