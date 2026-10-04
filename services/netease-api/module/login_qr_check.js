const createOption = require('../util/option.js')
module.exports = async (query, request) => {
  const data = {
    key: query.key,
    type: 3,
  }
  try {
    let result = await request(
      `/api/login/qrcode/client/login`,
      data,
      createOption(query),
    )
    result = {
      status: 200,
      body: {
        ...result.body,
        cookie: Array.isArray(result.cookie) ? result.cookie.join(';') : '',
      },
      cookie: result.cookie || [],
    }
    return result
  } catch (error) {
    // 修复：原实现 catch 内引用未定义的 result（ReferenceError），把任何上游
    // 失败都变成「无 body 的 reject」，被 server.js 统一映射为 HTTP 404，
    // 前端完全看不到真实原因。这里改为透传上游错误码并落一行日志。
    console.log(
      '[qr-check] upstream error:',
      error && (error.message || error.code),
      'status:',
      error && error.status,
      'body:',
      JSON.stringify(error && error.body).slice(0, 200),
    )
    return {
      status: 200,
      body: {
        code: (error && error.body && error.body.code) || 800,
        message:
          (error && error.body && error.body.message) || '二维码不存在或已过期',
      },
      cookie: [],
    }
  }
}
